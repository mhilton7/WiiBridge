package fat32virtual

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"wiibridge/shared/perf"
	"wiibridge/shared/sourceidentity"
)

var ErrSourceIdentityChanged = errors.New("GameCube source identity changed")

type cacheEntry struct {
	path string
	file *os.File
}

type Backend struct {
	mu                sync.Mutex
	size              int64
	metadata          []byte
	meta              []MetadataExtent
	extents           []Extent
	limit             int
	cache             map[string]*list.Element
	lru               *list.List
	closed            bool
	stats             Stats
	saves             []SaveExtent
	saveStore         SaveStore
	metrics           *perf.Registry
	onSourceFailure   func(string)
	lastSourceFailure atomic.Int64
	sourceFilesystems map[string]string
}

type Stats struct {
	SourceOpens   uint64
	SourceReads   uint64
	CacheHits     uint64
	PeakOpenFiles int
	CachedFiles   int
	CacheMisses   uint64
	Evictions     uint64
}

type SaveStore interface {
	ReadSaveAt(objectID string, buffer []byte, offset int64) (int, error)
	WriteSaveAt(objectID string, buffer []byte, offset int64) (int, error)
	Sync() error
	Close() error
}

type OpenOptions struct {
	// SourceFilesystems supplements legacy immutable layouts after receipt validation.
	SourceFilesystems map[string]string
	CacheLimit        int
	SaveStore         SaveStore
	Metrics           *perf.Registry
}

func Open(layout Layout, metadata []byte, cacheLimit int) (*Backend, error) {
	return OpenWithOptions(layout, metadata, OpenOptions{CacheLimit: cacheLimit})
}

func OpenWithOptions(layout Layout, metadata []byte, options OpenOptions) (*Backend, error) {
	if layout.Schema != 2 || layout.VirtualSize <= 0 || options.CacheLimit < 1 {
		return nil, errors.New("invalid virtual FAT32 backend configuration")
	}
	if len(layout.SaveExtents) > 0 && options.SaveStore == nil {
		return nil, errors.New("writable save extents require a save store")
	}
	if err := Validate(layout, metadata); err != nil {
		return nil, err
	}
	filesystems := make(map[string]string, len(options.SourceFilesystems))
	for path, id := range options.SourceFilesystems {
		filesystems[path] = id
	}
	return &Backend{sourceFilesystems: filesystems,
		size: layout.VirtualSize, metadata: append([]byte(nil), metadata...),
		meta:    append([]MetadataExtent(nil), layout.MetadataExtents...),
		extents: append([]Extent(nil), layout.SourceExtents...),
		saves:   append([]SaveExtent(nil), layout.SaveExtents...),
		limit:   options.CacheLimit, cache: make(map[string]*list.Element), lru: list.New(),
		saveStore: options.SaveStore, metrics: options.Metrics,
	}, nil
}

// Validate checks the complete immutable metadata and extent maps without
// allocating a backend or copying the metadata store. It does not authorize
// source access or writes; OpenWithOptions still requires a save store for
// writable extents and retains its own defensive copies.
func Validate(layout Layout, metadata []byte) error {
	if layout.Schema != 2 || layout.VirtualSize <= 0 {
		return errors.New("invalid virtual FAT32 backend configuration")
	}
	for _, extent := range layout.MetadataExtents {
		if extent.StorageOffset < 0 || extent.Length < 0 ||
			extent.StorageOffset > int64(len(metadata))-extent.Length {
			return errors.New("metadata extent exceeds metadata store")
		}
	}
	sum := sha256.Sum256(metadata)
	if hex.EncodeToString(sum[:]) != layout.MetadataHash ||
		hashExtents(layout.SourceExtents) != layout.ExtentMapHash {
		return errors.New("virtual FAT32 metadata or extent-map hash mismatch")
	}
	if len(layout.SaveExtents) > 0 {
		if hashSaveExtents(layout.SaveExtents, true) != layout.SaveExtentHash {
			return errors.New("virtual FAT32 save-extent hash mismatch")
		}
		baseSaveHash := hashSaveExtents(layout.SaveExtents, false)
		layoutSum := sha256.Sum256([]byte(
			layout.MetadataHash + "\x00" + layout.ExtentMapHash + "\x00" + baseSaveHash))
		if hex.EncodeToString(layoutSum[:]) != layout.LayoutChecksum {
			return errors.New("virtual FAT32 layout checksum mismatch")
		}
		for _, extent := range layout.SaveExtents {
			if extent.LayoutChecksum != layout.LayoutChecksum {
				return errors.New("writable extent layout checksum mismatch")
			}
		}
	}
	return validateRanges(layout.VirtualSize, layout.MetadataExtents,
		layout.SourceExtents, layout.SaveExtents)
}

func (b *Backend) Size() int64 { return b.size }
func (b *Backend) ReadOnly() bool {
	return len(b.saves) == 0 || b.saveStore == nil
}

func (b *Backend) metricsEnabled() bool {
	return b.metrics != nil && b.metrics.Enabled()
}

func (b *Backend) SetSourceFailureHandler(handler func(string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onSourceFailure = handler
}

type SaveWriteError struct {
	Code string
}

func (e *SaveWriteError) Error() string { return e.Code }
func (e *SaveWriteError) ErrorCode() string {
	return e.Code
}

func (b *Backend) WriteAt(buffer []byte, offset int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, os.ErrClosed
	}
	reject := func(code string) (int, error) {
		if b.metricsEnabled() {
			b.metrics.Disk.RejectedWrites.Add(1)
			b.metrics.Save.RejectedWrite.Add(1)
		}
		return 0, &SaveWriteError{Code: code}
	}
	if b.ReadOnly() {
		if b.metricsEnabled() {
			b.metrics.Disk.RejectedWrites.Add(1)
		}
		return 0, os.ErrPermission
	}
	if offset < 0 || len(buffer) == 0 || int64(len(buffer)) > b.size-offset {
		return reject("SAVE-WRITE-OUTSIDE-EXTENT")
	}
	extent, ok := findSave(b.saves, offset)
	if !ok {
		return reject("SAVE-WRITE-OUTSIDE-EXTENT")
	}
	if int64(len(buffer)) > extent.VirtualOffset+extent.Length-offset {
		return reject("SAVE-WRITE-CROSSES-BOUNDARY")
	}
	written, err := b.saveStore.WriteSaveAt(
		extent.SaveObjectID, buffer, extent.CardOffset+offset-extent.VirtualOffset)
	if err != nil {
		return written, err
	}
	if b.metricsEnabled() {
		b.metrics.Disk.SaveWrites.Add(1)
	}
	return written, nil
}

func (b *Backend) Sync() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return os.ErrClosed
	}
	if b.saveStore != nil {
		return b.saveStore.Sync()
	}
	return nil
}

func (b *Backend) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := b.stats
	result.CachedFiles = len(b.cache)
	return result
}

func (b *Backend) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset < 0 || int64(len(buffer)) > b.size-offset {
		return 0, io.EOF
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, os.ErrClosed
	}
	segments := 0
	lookupStarted := time.Time{}
	if b.metricsEnabled() {
		lookupStarted = time.Now()
	}
	for done := 0; done < len(buffer); {
		position := offset + int64(done)
		if b.metricsEnabled() {
			b.metrics.Disk.ExtentLookups.Add(1)
		}
		if item, ok := findSave(b.saves, position); ok {
			segments++
			count := len(buffer) - done
			available := item.VirtualOffset + item.Length - position
			if int64(count) > available {
				count = int(available)
			}
			read, readErr := b.saveStore.ReadSaveAt(
				item.SaveObjectID, buffer[done:done+count],
				item.CardOffset+position-item.VirtualOffset)
			done += read
			if b.metricsEnabled() {
				b.metrics.Disk.SaveReads.Add(1)
			}
			if readErr != nil {
				return done, readErr
			}
			continue
		}
		if item, ok := findMetadata(b.meta, position); ok {
			segments++
			count := len(buffer) - done
			available := item.VirtualOffset + item.Length - position
			if int64(count) > available {
				count = int(available)
			}
			source := item.StorageOffset + position - item.VirtualOffset
			copy(buffer[done:done+count], b.metadata[source:source+int64(count)])
			done += count
			if b.metricsEnabled() {
				b.metrics.Disk.MetadataReads.Add(1)
			}
			continue
		}
		if item, ok := findSource(b.extents, position); ok {
			segments++
			count := len(buffer) - done
			available := item.VirtualOffset + item.Length - position
			if int64(count) > available {
				count = int(available)
			}
			started := time.Now()
			expected := item.Identity
			if expected.FilesystemID == "" {
				expected.FilesystemID = b.sourceFilesystems[item.SourcePath]
			}
			if err := verifyIdentity(item.SourcePath, expected); err != nil {
				if b.metricsEnabled() {
					b.metrics.Source.IdentityErrors.Add(1)
					b.metrics.ObserveSourceRead(0, time.Since(started), err)
				}
				code := sourceFailureCode(err)
				if errors.Is(err, ErrSourceIdentityChanged) {
					code = "SOURCE-IDENTITY-CHANGED"
				}
				b.dropCached(item.SourcePath)
				b.reportSourceFailure(code)
				return done, err
			}
			file, err := b.open(item.SourcePath)
			if err != nil {
				if b.metricsEnabled() {
					b.metrics.ObserveSourceRead(0, time.Since(started), err)
				}
				b.reportSourceFailure(sourceFailureCode(err))
				return done, err
			}
			b.stats.SourceReads++
			read, readErr := readFullAt(file, buffer[done:done+count],
				item.SourceOffset+position-item.VirtualOffset)
			done += read
			if b.metricsEnabled() {
				b.metrics.Disk.PayloadReads.Add(1)
				b.metrics.ObserveSourceRead(read, time.Since(started), readErr)
			}
			if readErr != nil {
				b.dropCached(item.SourcePath)
				b.reportSourceFailure(sourceFailureCode(readErr))
				return done, readErr
			}
			continue
		}
		next := offset + int64(len(buffer))
		if index := sort.Search(len(b.meta), func(index int) bool {
			return b.meta[index].VirtualOffset > position
		}); index < len(b.meta) && b.meta[index].VirtualOffset < next {
			next = b.meta[index].VirtualOffset
		}
		if index := sort.Search(len(b.extents), func(index int) bool {
			return b.extents[index].VirtualOffset > position
		}); index < len(b.extents) && b.extents[index].VirtualOffset < next {
			next = b.extents[index].VirtualOffset
		}
		if index := sort.Search(len(b.saves), func(index int) bool {
			return b.saves[index].VirtualOffset > position
		}); index < len(b.saves) && b.saves[index].VirtualOffset < next {
			next = b.saves[index].VirtualOffset
		}
		count := int(next - position)
		if count < 1 {
			count = 1
		}
		clear(buffer[done : done+count])
		done += count
		if b.metricsEnabled() {
			b.metrics.Disk.ZeroReads.Add(1)
		}
	}
	if b.metricsEnabled() {
		b.metrics.Disk.LookupLatency.Observe(time.Since(lookupStarted))
		if segments > 1 {
			b.metrics.Disk.CrossExtent.Add(1)
		} else if segments == 1 {
			b.metrics.Disk.CoalescedReads.Add(1)
		}
	}
	return len(buffer), nil
}

func (b *Backend) reportSourceFailure(code string) {
	if b.onSourceFailure == nil {
		return
	}
	now := time.Now().UnixNano()
	last := b.lastSourceFailure.Load()
	if now-last < int64(10*time.Second) ||
		!b.lastSourceFailure.CompareAndSwap(last, now) {
		return
	}
	b.onSourceFailure(code)
}

func sourceFailureCode(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "SOURCE-PERMISSION-DENIED"
	}
	return "SOURCE-READ-FAILED"
}

func findMetadata(items []MetadataExtent, position int64) (MetadataExtent, bool) {
	index := sort.Search(len(items), func(index int) bool {
		return items[index].VirtualOffset+items[index].Length > position
	})
	if index < len(items) && position >= items[index].VirtualOffset {
		return items[index], true
	}
	return MetadataExtent{}, false
}

func findSource(items []Extent, position int64) (Extent, bool) {
	index := sort.Search(len(items), func(index int) bool {
		return items[index].VirtualOffset+items[index].Length > position
	})
	if index < len(items) && position >= items[index].VirtualOffset {
		return items[index], true
	}
	return Extent{}, false
}

func findSave(items []SaveExtent, position int64) (SaveExtent, bool) {
	index := sort.Search(len(items), func(index int) bool {
		return items[index].VirtualOffset+items[index].Length > position
	})
	if index < len(items) && position >= items[index].VirtualOffset {
		return items[index], true
	}
	return SaveExtent{}, false
}

func verifyIdentity(path string, expected Identity) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != expected.Size || info.ModTime().UnixNano() != expected.ModTimeUnixNano {
		return ErrSourceIdentityChanged
	}
	filesystemID := ""
	if expected.FilesystemID != "" {
		filesystemID, err = sourceidentity.FilesystemID(path)
		if err != nil {
			return err
		}
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok ||
		(!sourceidentity.SameFilesystem(expected.FilesystemID, expected.Device, filesystemID, uint64(stat.Dev)) || stat.Ino != expected.Inode) {
		return ErrSourceIdentityChanged
	}
	return nil
}

func (b *Backend) open(path string) (*os.File, error) {
	if element := b.cache[path]; element != nil {
		b.stats.CacheHits++
		if b.metricsEnabled() {
			b.metrics.Source.CacheHits.Add(1)
		}
		b.lru.MoveToFront(element)
		return element.Value.(*cacheEntry).file, nil
	}
	if b.lru.Len() >= b.limit {
		oldest := b.lru.Back()
		entry := oldest.Value.(*cacheEntry)
		delete(b.cache, entry.path)
		b.lru.Remove(oldest)
		if err := entry.file.Close(); err != nil {
			return nil, err
		}
		b.stats.Evictions++
		if b.metricsEnabled() {
			b.metrics.Source.Evictions.Add(1)
			b.metrics.Source.OpenHandles.Add(-1)
		}
	}
	b.stats.CacheMisses++
	if b.metricsEnabled() {
		b.metrics.Source.CacheMisses.Add(1)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	b.stats.SourceOpens++
	if b.metricsEnabled() {
		b.metrics.Source.OpenHandles.Add(1)
	}
	element := b.lru.PushFront(&cacheEntry{path: path, file: file})
	b.cache[path] = element
	if len(b.cache) > b.stats.PeakOpenFiles {
		b.stats.PeakOpenFiles = len(b.cache)
	}
	return file, nil
}

func (b *Backend) dropCached(path string) {
	element := b.cache[path]
	if element == nil {
		return
	}
	entry := element.Value.(*cacheEntry)
	delete(b.cache, path)
	b.lru.Remove(element)
	_ = entry.file.Close()
	b.stats.Evictions++
	if b.metricsEnabled() {
		b.metrics.Source.Evictions.Add(1)
		b.metrics.Source.OpenHandles.Add(-1)
	}
}

func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	var first error
	for _, element := range b.cache {
		if err := element.Value.(*cacheEntry).file.Close(); err != nil && first == nil {
			first = err
		}
	}
	if b.metricsEnabled() {
		b.metrics.Source.OpenHandles.Store(0)
	}
	b.cache = nil
	b.lru.Init()
	if b.saveStore != nil {
		if err := b.saveStore.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func readFullAt(reader io.ReaderAt, buffer []byte, offset int64) (int, error) {
	done := 0
	for done < len(buffer) {
		count, err := reader.ReadAt(buffer[done:], offset+int64(done))
		done += count
		if done == len(buffer) {
			return done, nil
		}
		if err != nil {
			return done, err
		}
		if count == 0 {
			return done, io.ErrNoProgress
		}
	}
	return done, nil
}
