package gamecube

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"wiibridge/shared/perf"
)

const (
	SaveOverlayFormatVersion = 1
	SaveBlockSize            = int64(512)
	DefaultLibraryCardSize   = int64(16 << 20)
	MaximumSaveCardSize      = int64(16 << 20)
	MaximumSaveUploadSize    = int64(16 << 20)
	DefaultSaveJournalLimit  = int64(64 << 20)
	DefaultPendingSaveBytes  = int64(16 << 20)
	DefaultDirtyBlockLimit   = 32_768
)

var (
	saveObjectPattern = regexp.MustCompile(`^(?:individual:[A-Z0-9]{6}|shared:[A-Za-z0-9_-]{1,32})$`)
	sharedCardPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	journalMagic      = [8]byte{'W', 'B', 'S', 'A', 'V', 'E', '1', '\n'}
)

type SaveObject struct {
	ID             string         `json:"id"`
	Mode           MemoryCardMode `json:"mode"`
	GameID         string         `json:"game_id,omitempty"`
	SharedCardName string         `json:"shared_card_name,omitempty"`
	CardSize       int64          `json:"card_size"`
	VirtualPath    string         `json:"virtual_path"`
}

type SaveMetadata struct {
	FormatVersion    int            `json:"formatVersion"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	Mode             MemoryCardMode `json:"mode"`
	GameID           string         `json:"gameId,omitempty"`
	SharedCardName   string         `json:"sharedCardName,omitempty"`
	CardSize         int64          `json:"cardSize"`
	SHA256           string         `json:"sha256"`
	Application      string         `json:"applicationVersion"`
	GenerationID     string         `json:"generationId"`
	LastFlush        time.Time      `json:"lastSuccessfulFlush,omitempty"`
	LastBackup       time.Time      `json:"lastSuccessfulBackup,omitempty"`
	Dirty            bool           `json:"dirty"`
	RecoveryState    string         `json:"recoveryState"`
	Device           uint64         `json:"device"`
	Inode            uint64         `json:"inode"`
	ModTimeUnixNano  int64          `json:"mtimeUnixNano"`
	LastErrorCode    string         `json:"lastErrorCode,omitempty"`
	LastErrorMessage string         `json:"lastError,omitempty"`
}

type BackupMetadata struct {
	FormatVersion  int            `json:"formatVersion"`
	CreatedAt      time.Time      `json:"createdAt"`
	Mode           MemoryCardMode `json:"mode"`
	GameID         string         `json:"gameId,omitempty"`
	SharedCardName string         `json:"sharedCardName,omitempty"`
	CardSize       int64          `json:"cardSize"`
	SHA256         string         `json:"sha256"`
	Application    string         `json:"applicationVersion"`
	GenerationID   string         `json:"generationId"`
	Reason         string         `json:"reason"`
	Name           string         `json:"name"`
}

type SaveStatus struct {
	SaveObject
	IntegrityState string    `json:"integrity_state"`
	Dirty          bool      `json:"dirty"`
	RecoveryState  string    `json:"recovery_state"`
	LastFlush      time.Time `json:"last_successful_flush,omitempty"`
	LastBackup     time.Time `json:"last_successful_backup,omitempty"`
	BackupCount    int       `json:"backup_count"`
	CurrentSHA256  string    `json:"current_checksum"`
	JournalBytes   int64     `json:"journal_bytes"`
	DirtyBlocks    int       `json:"dirty_blocks"`
	CurrentError   string    `json:"current_error,omitempty"`
	ErrorCode      string    `json:"error_code,omitempty"`
}

type SaveStoreConfig struct {
	Root            string
	Application     string
	GenerationID    string
	LayoutChecksum  string
	MaxBackups      int
	MaxDirtyBlocks  int
	MaxJournalBytes int64
	MaxPendingBytes int64
	Metrics         *perf.Registry
}

type SaveSelection struct {
	FormatVersion           int            `json:"formatVersion"`
	Mode                    MemoryCardMode `json:"mode"`
	CardSize                int64          `json:"cardSize"`
	SharedCardName          string         `json:"sharedCardName,omitempty"`
	AutomaticCreation       bool           `json:"automaticCreation"`
	MaximumRetainedBackups  int            `json:"maximumRetainedBackups"`
	AutomaticBackupInterval int64          `json:"automaticBackupIntervalSeconds,omitempty"`
	UpdatedAt               time.Time      `json:"updatedAt"`
}

func (selection SaveSelection) Validate() error {
	if selection.FormatVersion != SaveOverlayFormatVersion {
		return errors.New("SAVE-CARD-INVALID: unsupported save settings format")
	}
	if selection.Mode != MemoryCardPhysical && !selection.Mode.IsLibraryEmulated() {
		return errors.New("SAVE-CARD-INVALID: unsupported memory-card mode")
	}
	if !SupportedSaveCardSize(selection.CardSize) {
		return errors.New("SAVE-CARD-SIZE-UNSUPPORTED")
	}
	if selection.Mode == MemoryCardEmulatedShared &&
		!sharedCardPattern.MatchString(selection.SharedCardName) {
		return errors.New("SAVE-CARD-INVALID: shared memory-card selection")
	}
	if selection.MaximumRetainedBackups < 1 ||
		selection.MaximumRetainedBackups > 100 {
		return errors.New("SAVE-BACKUP-FAILED: backup retention must be between 1 and 100")
	}
	if selection.AutomaticBackupInterval < 0 ||
		selection.AutomaticBackupInterval > int64((30*24*time.Hour)/time.Second) {
		return errors.New("SAVE-BACKUP-FAILED: invalid automatic backup interval")
	}
	return nil
}

func LoadSaveSelection(path string, fallback SaveSelection) (SaveSelection, error) {
	if err := fallback.Validate(); err != nil {
		return SaveSelection{}, err
	}
	data, err := readRegularBounded(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return fallback, nil
	}
	if err != nil {
		// readRegularBounded deliberately hides filesystem details. Distinguish
		// an absent first-run file without accepting symlinks or special files.
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return fallback, nil
		}
		return SaveSelection{}, err
	}
	var selection SaveSelection
	if err = json.Unmarshal(data, &selection); err != nil {
		return SaveSelection{}, errors.New("SAVE-CARD-INVALID: malformed save settings")
	}
	if err = selection.Validate(); err != nil {
		return SaveSelection{}, err
	}
	return selection, nil
}

func SaveSaveSelection(path string, selection SaveSelection) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	selection.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return err
	}
	if err = safeDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'))
}

type SaveStore struct {
	mu      sync.RWMutex
	config  SaveStoreConfig
	objects map[string]*cardObject
	closed  bool
}

func (store *SaveStore) metricsEnabled() bool {
	return store.config.Metrics != nil && store.config.Metrics.Enabled()
}

type cardObject struct {
	spec         SaveObject
	directory    string
	activePath   string
	metadataPath string
	journalPath  string
	file         *os.File
	journal      *os.File
	metadata     SaveMetadata
	dirty        map[int64][]byte
	pendingBytes int64
	journalBytes int64
	blocked      bool
}

func SupportedSaveCardSize(size int64) bool {
	return validMemoryCardSizes[size] && size <= MaximumSaveCardSize
}

func PlanSaveObjects(mode MemoryCardMode, games []Game, sharedName string,
	cardSize int64,
) ([]SaveObject, error) {
	if !mode.IsLibraryEmulated() {
		if mode == MemoryCardPhysical {
			return nil, nil
		}
		return nil, errors.New("unsupported complete-library memory-card mode")
	}
	if !SupportedSaveCardSize(cardSize) {
		return nil, fmt.Errorf("SAVE-CARD-SIZE-UNSUPPORTED: %d", cardSize)
	}
	if mode == MemoryCardEmulatedShared {
		if !sharedCardPattern.MatchString(sharedName) {
			return nil, errors.New("shared memory-card name must use letters, numbers, underscore, or hyphen")
		}
		return []SaveObject{{
			ID: "shared:" + sharedName, Mode: mode, SharedCardName: sharedName,
			CardSize: cardSize, VirtualPath: "/saves/ninmem.raw",
		}}, nil
	}
	objects := make([]SaveObject, 0, len(games))
	seen := make(map[string]struct{}, len(games))
	for _, game := range games {
		if !validID.MatchString(game.ID) {
			return nil, errors.New("invalid GameCube ID for individual memory card")
		}
		id := "individual:" + game.ID
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		objects = append(objects, SaveObject{
			ID: id, Mode: mode, GameID: game.ID, CardSize: cardSize,
			VirtualPath: "/saves/" + game.ID + ".raw",
		})
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].ID < objects[j].ID })
	return objects, nil
}

func EnsureSaveObjects(root string, objects []SaveObject, autoCreate bool,
	application, generation string,
) error {
	for _, object := range objects {
		directory, err := saveObjectDirectory(root, object)
		if err != nil {
			return err
		}
		active := filepath.Join(directory, "active.raw")
		if _, err = os.Lstat(active); errors.Is(err, os.ErrNotExist) {
			if !autoCreate {
				return fmt.Errorf("SAVE-CARD-MISSING: %s", object.ID)
			}
			if err = createCard(root, directory, object, application, generation); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err = validateCardFiles(directory, object); err != nil {
			return err
		}
	}
	return nil
}

func createCard(root, directory string, object SaveObject, application, generation string) error {
	if !SupportedSaveCardSize(object.CardSize) {
		return errors.New("SAVE-CARD-SIZE-UNSUPPORTED")
	}
	if err := ensureManagedDirectoryTree(root, directory); err != nil {
		return err
	}
	if err := ensureManagedDirectoryTree(directory, filepath.Join(directory, "journal")); err != nil {
		return err
	}
	if err := ensureManagedDirectoryTree(directory, filepath.Join(directory, "backups")); err != nil {
		return err
	}
	active := filepath.Join(directory, "active.raw")
	temp, err := os.OpenFile(filepath.Join(directory, ".create.tmp"),
		os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err = temp.Truncate(object.CardSize); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tempPath, active); err != nil {
		return err
	}
	info, err := os.Lstat(active)
	if err != nil {
		return err
	}
	metadata := SaveMetadata{
		FormatVersion: SaveOverlayFormatVersion, CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(), Mode: object.Mode, GameID: object.GameID,
		SharedCardName: object.SharedCardName, CardSize: object.CardSize,
		SHA256: zeroCardHash(object.CardSize), Application: application,
		GenerationID: generation, RecoveryState: "clean",
	}
	setFileIdentity(&metadata, info)
	if err = writeSaveMetadata(filepath.Join(directory, "metadata.json"), metadata); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func OpenSaveStore(config SaveStoreConfig, objects []SaveObject) (*SaveStore, error) {
	if config.Root == "" || config.GenerationID == "" || config.LayoutChecksum == "" {
		return nil, errors.New("invalid save-store generation configuration")
	}
	if config.MaxBackups < 1 {
		config.MaxBackups = DefaultSaveBackupRetention
	}
	if config.MaxDirtyBlocks < 1 {
		config.MaxDirtyBlocks = DefaultDirtyBlockLimit
	}
	if config.MaxJournalBytes < 1 {
		config.MaxJournalBytes = DefaultSaveJournalLimit
	}
	if config.MaxPendingBytes < 1 {
		config.MaxPendingBytes = DefaultPendingSaveBytes
	}
	store := &SaveStore{config: config, objects: make(map[string]*cardObject, len(objects))}
	for _, spec := range objects {
		card, err := store.openObject(spec)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		store.objects[spec.ID] = card
	}
	return store, nil
}

func (store *SaveStore) openObject(spec SaveObject) (*cardObject, error) {
	directory, err := saveObjectDirectory(store.config.Root, spec)
	if err != nil {
		return nil, err
	}
	if err = recoverInterruptedActivation(directory, spec); err != nil {
		return nil, err
	}
	metadata, err := validateCardFiles(directory, spec)
	if err != nil {
		return nil, err
	}
	if metadata.FormatVersion != SaveOverlayFormatVersion {
		return nil, errors.New("SAVE-CARD-INVALID: unsupported save-overlay format")
	}
	activePath := filepath.Join(directory, "active.raw")
	file, err := os.Open(activePath)
	if err != nil {
		return nil, err
	}
	card := &cardObject{
		spec: spec, directory: directory, activePath: activePath,
		metadataPath: filepath.Join(directory, "metadata.json"),
		journalPath:  filepath.Join(directory, "journal", "pending.log"),
		file:         file, metadata: metadata, dirty: make(map[int64][]byte),
	}
	if err = rejectCheckpointConflicts(directory); err != nil {
		file.Close()
		return nil, err
	}
	if err = card.openJournal(); err != nil {
		file.Close()
		return nil, err
	}
	if card.journalBytes > 0 {
		if err = store.loadJournal(card); err != nil {
			card.journal.Close()
			card.file.Close()
			return nil, err
		}
		card.metadata.RecoveryState = "journal-recovery"
		if err = store.flushCard(card); err != nil {
			card.journal.Close()
			card.file.Close()
			return nil, fmt.Errorf("SAVE-RECOVERY-AMBIGUOUS: %w", err)
		}
		if store.metricsEnabled() {
			store.config.Metrics.Save.RecoveryCount.Add(1)
		}
	}
	return card, nil
}

func (card *cardObject) openJournal() error {
	info, err := os.Lstat(filepath.Dir(card.journalPath))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("SAVE-CARD-INVALID: journal directory")
	}
	file, err := os.OpenFile(card.journalPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		file.Close()
		return errors.New("SAVE-CARD-INVALID: journal file")
	}
	card.journal, card.journalBytes = file, info.Size()
	return nil
}

func (store *SaveStore) ReadSaveAt(objectID string, buffer []byte, offset int64) (int, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed {
		return 0, os.ErrClosed
	}
	card := store.objects[objectID]
	if card == nil || offset < 0 || int64(len(buffer)) > card.spec.CardSize-offset {
		return 0, errors.New("SAVE-WRITE-OUTSIDE-EXTENT")
	}
	if len(card.dirty) == 0 {
		return card.file.ReadAt(buffer, offset)
	}
	done := 0
	for done < len(buffer) {
		position := offset + int64(done)
		blockIndex := position / SaveBlockSize
		inBlock := position % SaveBlockSize
		length := min(int64(len(buffer)-done), SaveBlockSize-inBlock)
		if block := card.dirty[blockIndex]; block != nil {
			copy(buffer[done:done+int(length)], block[inBlock:inBlock+length])
		} else {
			// Read an entire contiguous clean span. Dirty blocks remain the
			// authority for their bytes and are never read from the base card.
			remaining := int64(len(buffer) - done)
			for length < remaining && card.dirty[(position+length)/SaveBlockSize] == nil {
				length += min(remaining-length, SaveBlockSize)
			}
			count, err := card.file.ReadAt(buffer[done:done+int(length)], position)
			done += count
			if err != nil {
				return done, err
			}
			continue
		}
		done += int(length)
	}
	return done, nil
}

func (store *SaveStore) WriteSaveAt(objectID string, buffer []byte, offset int64) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return 0, os.ErrClosed
	}
	card := store.objects[objectID]
	if card == nil || offset < 0 || len(buffer) == 0 ||
		int64(len(buffer)) > card.spec.CardSize-offset {
		return 0, errors.New("SAVE-WRITE-OUTSIDE-EXTENT")
	}
	if card.blocked {
		return 0, errors.New("SAVE-RECOVERY-AMBIGUOUS: card journal is blocked")
	}
	firstBlock := offset / SaveBlockSize
	lastBlock := (offset + int64(len(buffer)) - 1) / SaveBlockSize
	requestBlocks := int(lastBlock-firstBlock) + 1
	if int64(len(buffer)) > store.config.MaxPendingBytes ||
		int64(len(buffer))+52 > store.config.MaxJournalBytes ||
		requestBlocks > store.config.MaxDirtyBlocks {
		return 0, errors.New("SAVE-JOURNAL-LIMIT")
	}
	if card.pendingBytes+int64(len(buffer)) > store.config.MaxPendingBytes ||
		card.journalBytes+int64(len(buffer))+52 > store.config.MaxJournalBytes {
		if err := store.flushCard(card); err != nil {
			return 0, fmt.Errorf("SAVE-JOURNAL-LIMIT: %w", err)
		}
	}
	newBlocks := 0
	for block := firstBlock; block <= lastBlock; block++ {
		if card.dirty[block] == nil {
			newBlocks++
		}
	}
	if len(card.dirty)+newBlocks > store.config.MaxDirtyBlocks {
		if err := store.flushCard(card); err != nil {
			return 0, fmt.Errorf("SAVE-JOURNAL-LIMIT: %w", err)
		}
	}
	if err := appendJournal(card, offset, buffer); err != nil {
		card.blocked = true
		card.metadata.RecoveryState = "journal-incomplete"
		card.metadata.LastErrorCode = "SAVE-FLUSH-FAILED"
		card.metadata.LastErrorMessage = "The bounded save journal is incomplete; restart recovery is required."
		return 0, fmt.Errorf("SAVE-FLUSH-FAILED: journal: %w", err)
	}
	for done := 0; done < len(buffer); {
		position := offset + int64(done)
		blockIndex := position / SaveBlockSize
		inBlock := position % SaveBlockSize
		length := min(int64(len(buffer)-done), SaveBlockSize-inBlock)
		block := card.dirty[blockIndex]
		if block == nil {
			block = make([]byte, SaveBlockSize)
			if _, err := card.file.ReadAt(block, blockIndex*SaveBlockSize); err != nil {
				return done, err
			}
			card.dirty[blockIndex] = block
		}
		copy(block[inBlock:inBlock+length], buffer[done:done+int(length)])
		done += int(length)
	}
	card.pendingBytes += int64(len(buffer))
	card.journalBytes += int64(len(buffer)) + 52
	card.metadata.Dirty = true
	card.metadata.UpdatedAt = time.Now().UTC()
	if store.metricsEnabled() {
		store.config.Metrics.Save.DirtyBlocks.Store(int64(store.totalDirtyBlocks()))
		store.config.Metrics.Save.DirtyBytes.Store(int64(store.totalDirtyBlocks()) * SaveBlockSize)
		store.config.Metrics.Save.JournalBytes.Store(store.totalJournalBytes())
	}
	return len(buffer), nil
}

func appendJournal(card *cardObject, offset int64, buffer []byte) error {
	var header [52]byte
	copy(header[:8], journalMagic[:])
	binary.LittleEndian.PutUint64(header[8:16], uint64(offset))
	binary.LittleEndian.PutUint32(header[16:20], uint32(len(buffer)))
	sum := sha256.Sum256(buffer)
	copy(header[20:52], sum[:])
	if _, err := card.journal.Write(header[:]); err != nil {
		return err
	}
	if _, err := card.journal.Write(buffer); err != nil {
		return err
	}
	return card.journal.Sync()
}

func (store *SaveStore) loadJournal(card *cardObject) error {
	if card.journalBytes > store.config.MaxJournalBytes {
		return errors.New("journal exceeds configured limit")
	}
	if _, err := card.journal.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(io.LimitReader(card.journal, store.config.MaxJournalBytes+1))
	var total int64
	for {
		var header [52]byte
		_, err := io.ReadFull(reader, header[:])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || !bytes.Equal(header[:8], journalMagic[:]) {
			return errors.New("incomplete or malformed save journal")
		}
		offset := int64(binary.LittleEndian.Uint64(header[8:16]))
		length := int64(binary.LittleEndian.Uint32(header[16:20]))
		if length <= 0 || length > MaximumSaveUploadSize ||
			offset < 0 || offset > card.spec.CardSize-length ||
			total > store.config.MaxPendingBytes-length {
			return errors.New("save journal record exceeds bounds")
		}
		data := make([]byte, length)
		if _, err = io.ReadFull(reader, data); err != nil {
			return errors.New("incomplete save journal record")
		}
		sum := sha256.Sum256(data)
		if !bytes.Equal(sum[:], header[20:52]) {
			return errors.New("save journal checksum mismatch")
		}
		for done := int64(0); done < length; {
			position := offset + done
			blockIndex := position / SaveBlockSize
			inBlock := position % SaveBlockSize
			count := min(length-done, SaveBlockSize-inBlock)
			block := card.dirty[blockIndex]
			if block == nil {
				block = make([]byte, SaveBlockSize)
				if _, err = card.file.ReadAt(block, blockIndex*SaveBlockSize); err != nil {
					return err
				}
				card.dirty[blockIndex] = block
			}
			copy(block[inBlock:inBlock+count], data[done:done+count])
			done += count
		}
		total += length
	}
	card.pendingBytes = total
	return nil
}

func (store *SaveStore) Sync() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return os.ErrClosed
	}
	keys := make([]string, 0, len(store.objects))
	for key := range store.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := store.flushCard(store.objects[key]); err != nil {
			return err
		}
	}
	return nil
}

func (store *SaveStore) flushCard(card *cardObject) error {
	if len(card.dirty) == 0 {
		return nil
	}
	started := time.Now()
	fail := func(err error) error {
		card.metadata.LastErrorCode = "SAVE-FLUSH-FAILED"
		card.metadata.LastErrorMessage = "Save checkpoint could not be committed."
		if store.metricsEnabled() {
			store.config.Metrics.Save.FlushFailures.Add(1)
		}
		return err
	}
	tempPath := filepath.Join(card.directory, ".checkpoint.tmp")
	if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	source, err := os.Open(card.activePath)
	if err != nil {
		return fail(err)
	}
	temp, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		source.Close()
		return fail(err)
	}
	_, copyErr := io.CopyN(temp, source, card.spec.CardSize)
	closeSourceErr := source.Close()
	if copyErr == nil {
		copyErr = closeSourceErr
	}
	keys := make([]int64, 0, len(card.dirty))
	for block := range card.dirty {
		keys = append(keys, block)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, block := range keys {
		if copyErr == nil {
			_, copyErr = temp.WriteAt(card.dirty[block], block*SaveBlockSize)
		}
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	if closeErr := temp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		os.Remove(tempPath)
		return fail(copyErr)
	}
	sum, err := hashRegularFile(tempPath, card.spec.CardSize)
	if err != nil {
		os.Remove(tempPath)
		return fail(err)
	}
	previousMetadata := card.metadata
	if err = preparePrevious(card); err != nil {
		os.Remove(tempPath)
		return fail(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = restorePrevious(card, previousMetadata)
		}
	}()
	if err = card.file.Close(); err != nil {
		return fail(err)
	}
	if err = os.Rename(tempPath, card.activePath); err != nil {
		card.file, _ = os.Open(card.activePath)
		return fail(err)
	}
	if err = syncDirectory(card.directory); err != nil {
		return fail(err)
	}
	card.file, err = os.Open(card.activePath)
	if err != nil {
		return fail(err)
	}
	info, err := card.file.Stat()
	if err != nil {
		return fail(err)
	}
	card.metadata.SHA256 = sum
	card.metadata.UpdatedAt, card.metadata.LastFlush = time.Now().UTC(), time.Now().UTC()
	card.metadata.Dirty, card.metadata.RecoveryState = false, "clean"
	card.metadata.GenerationID = store.config.GenerationID
	card.metadata.LastErrorCode, card.metadata.LastErrorMessage = "", ""
	setFileIdentity(&card.metadata, info)
	if err = writeSaveMetadata(card.metadataPath, card.metadata); err != nil {
		return fail(err)
	}
	if err = commitPrevious(card.directory); err != nil {
		return fail(err)
	}
	committed = true
	if err = card.journal.Truncate(0); err == nil {
		_, err = card.journal.Seek(0, io.SeekStart)
	}
	if err == nil {
		err = card.journal.Sync()
	}
	if err != nil {
		// The active card and metadata are already confirmed. Leaving the
		// idempotent journal intact lets startup replay it without data loss.
		return fail(err)
	}
	card.dirty = make(map[int64][]byte)
	card.pendingBytes, card.journalBytes = 0, 0
	if store.metricsEnabled() {
		store.config.Metrics.Save.FlushCount.Add(1)
		store.config.Metrics.Save.FlushLatency.Observe(time.Since(started))
		store.config.Metrics.RecordSaveFlush()
		store.config.Metrics.Save.DirtyBlocks.Store(int64(store.totalDirtyBlocks()))
		store.config.Metrics.Save.DirtyBytes.Store(int64(store.totalDirtyBlocks()) * SaveBlockSize)
		store.config.Metrics.Save.JournalBytes.Store(store.totalJournalBytes())
	}
	return nil
}

func (store *SaveStore) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	var first error
	for _, card := range store.objects {
		if len(card.dirty) > 0 {
			if err := store.flushCard(card); err != nil && first == nil {
				first = err
			}
		}
		if card.journal != nil {
			if err := card.journal.Close(); err != nil && first == nil {
				first = err
			}
		}
		if card.file != nil {
			if err := card.file.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	store.closed = true
	return first
}

func (store *SaveStore) Verify(objectID string) (SaveStatus, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	card := store.objects[objectID]
	if card == nil {
		return SaveStatus{}, errors.New("SAVE-CARD-MISSING")
	}
	if len(card.dirty) > 0 {
		if err := store.flushCard(card); err != nil {
			return SaveStatus{}, err
		}
	}
	sum, err := hashRegularFile(card.activePath, card.spec.CardSize)
	if err != nil || sum != card.metadata.SHA256 {
		return SaveStatus{}, errors.New("SAVE-CARD-INVALID: checksum mismatch")
	}
	return store.statusLocked(card)
}

func (store *SaveStore) Statuses() []SaveStatus {
	store.mu.RLock()
	defer store.mu.RUnlock()
	keys := make([]string, 0, len(store.objects))
	for key := range store.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SaveStatus, 0, len(keys))
	for _, key := range keys {
		status, _ := store.statusLocked(store.objects[key])
		result = append(result, status)
	}
	return result
}

func (store *SaveStore) statusLocked(card *cardObject) (SaveStatus, error) {
	backups, err := listBackupMetadata(card.directory, card.spec)
	if err != nil {
		return SaveStatus{}, err
	}
	status := SaveStatus{
		SaveObject: card.spec, IntegrityState: "valid", Dirty: len(card.dirty) > 0,
		RecoveryState: card.metadata.RecoveryState, LastFlush: card.metadata.LastFlush,
		LastBackup: card.metadata.LastBackup, BackupCount: len(backups),
		CurrentSHA256: card.metadata.SHA256, JournalBytes: card.journalBytes,
		DirtyBlocks: len(card.dirty), CurrentError: card.metadata.LastErrorMessage,
		ErrorCode: card.metadata.LastErrorCode,
	}
	return status, nil
}

func (store *SaveStore) Backup(objectID, reason string) (BackupMetadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return BackupMetadata{}, os.ErrClosed
	}
	card := store.objects[objectID]
	if card == nil {
		return BackupMetadata{}, errors.New("SAVE-CARD-MISSING")
	}
	if len(card.dirty) > 0 {
		if err := store.flushCard(card); err != nil {
			return BackupMetadata{}, err
		}
	}
	result, err := store.backupCard(card, reason)
	if err != nil {
		if store.metricsEnabled() {
			store.config.Metrics.Save.BackupFailure.Add(1)
		}
		return BackupMetadata{}, err
	}
	if store.metricsEnabled() {
		store.config.Metrics.Save.BackupCount.Add(1)
	}
	return result, nil
}

func (store *SaveStore) backupCard(card *cardObject, reason string) (BackupMetadata, error) {
	if reason == "" || len(reason) > 32 {
		return BackupMetadata{}, errors.New("SAVE-BACKUP-FAILED: invalid reason")
	}
	directory := filepath.Join(card.directory, "backups")
	if err := safeDirectory(directory); err != nil {
		return BackupMetadata{}, err
	}
	now := time.Now().UTC()
	base := now.Format("20060102T150405.000000000Z") + "-" + card.metadata.SHA256[:16]
	rawName := base + ".raw"
	temp, err := os.OpenFile(filepath.Join(directory, "."+base+".tmp"),
		os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return BackupMetadata{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	source, err := os.Open(card.activePath)
	if err == nil {
		_, err = io.CopyN(temp, source, card.spec.CardSize)
	}
	if source != nil {
		if closeErr := source.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return BackupMetadata{}, err
	}
	if err = validateCandidate(tempPath, card.spec.CardSize, card.metadata.SHA256); err != nil {
		return BackupMetadata{}, err
	}
	if err = os.Rename(tempPath, filepath.Join(directory, rawName)); err != nil {
		return BackupMetadata{}, err
	}
	metadata := BackupMetadata{
		FormatVersion: SaveOverlayFormatVersion, CreatedAt: now, Mode: card.spec.Mode,
		GameID: card.spec.GameID, SharedCardName: card.spec.SharedCardName,
		CardSize: card.spec.CardSize, SHA256: card.metadata.SHA256,
		Application: store.config.Application, GenerationID: store.config.GenerationID,
		Reason: reason, Name: rawName,
	}
	data, _ := json.MarshalIndent(metadata, "", "  ")
	if err = atomicWrite(filepath.Join(directory, base+".json"), append(data, '\n')); err != nil {
		_ = os.Remove(filepath.Join(directory, rawName))
		_ = syncDirectory(directory)
		return BackupMetadata{}, err
	}
	if err = syncDirectory(directory); err != nil {
		return BackupMetadata{}, err
	}
	confirmed, confirmErr := loadBackupMetadata(filepath.Join(directory, base+".json"))
	if confirmErr != nil || confirmed.SHA256 != metadata.SHA256 ||
		validateCandidate(
			filepath.Join(directory, rawName), card.spec.CardSize, metadata.SHA256) != nil {
		return BackupMetadata{}, errors.New("SAVE-BACKUP-FAILED: backup validation failed")
	}
	card.metadata.LastBackup = now
	if err = writeSaveMetadata(card.metadataPath, card.metadata); err != nil {
		return BackupMetadata{}, err
	}
	if err = pruneBackups(directory, store.config.MaxBackups); err != nil {
		return BackupMetadata{}, err
	}
	return metadata, nil
}

func (store *SaveStore) Restore(objectID, backupName string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return os.ErrClosed
	}
	card := store.objects[objectID]
	if card == nil {
		return errors.New("SAVE-CARD-MISSING")
	}
	if filepath.Base(backupName) != backupName || !strings.HasSuffix(backupName, ".raw") ||
		strings.HasPrefix(backupName, ".") {
		return errors.New("SAVE-RESTORE-FAILED: unsafe backup name")
	}
	backupPath := filepath.Join(card.directory, "backups", backupName)
	metadataPath := strings.TrimSuffix(backupPath, ".raw") + ".json"
	backup, err := loadBackupMetadata(metadataPath)
	if err != nil || backup.Name != backupName || !backupMatches(backup, card.spec) {
		return errors.New("SAVE-RESTORE-FAILED: backup association is invalid")
	}
	if err = validateCandidate(backupPath, card.spec.CardSize, backup.SHA256); err != nil {
		return fmt.Errorf("SAVE-RESTORE-FAILED: %w", err)
	}
	// Stage the selected candidate before creating the safety backup. Backup
	// pruning is allowed only after a new backup is complete; without this
	// staging step it could prune the selected old backup before activation.
	selectedPath := filepath.Join(card.directory, ".restore-selected.tmp")
	if err = copyValidatedCandidate(
		backupPath, selectedPath, card.spec.CardSize, backup.SHA256); err != nil {
		return fmt.Errorf("SAVE-RESTORE-FAILED: stage selected backup: %w", err)
	}
	defer os.Remove(selectedPath)
	if len(card.dirty) > 0 {
		if err = store.flushCard(card); err != nil {
			return err
		}
	}
	if _, err = store.backupCard(card, "pre-restore"); err != nil {
		return fmt.Errorf("SAVE-RESTORE-FAILED: safety backup: %w", err)
	}
	if err = store.activateCandidate(card, selectedPath, backup.SHA256); err != nil {
		return fmt.Errorf("SAVE-RESTORE-FAILED: %w", err)
	}
	if store.metricsEnabled() {
		store.config.Metrics.Save.RestoreCount.Add(1)
	}
	return nil
}

func copyValidatedCandidate(sourcePath, targetPath string, size int64, checksum string) error {
	if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil || !sourceInfo.Mode().IsRegular() ||
		sourceInfo.Mode()&os.ModeSymlink != 0 || sourceInfo.Size() != size {
		return errors.New("invalid restore source")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	copyErr := error(nil)
	if _, copyErr = io.CopyN(target, source, size); copyErr == nil {
		copyErr = target.Sync()
	}
	if closeErr := target.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(targetPath)
		return copyErr
	}
	return validateCandidate(targetPath, size, checksum)
}

func (store *SaveStore) Upload(objectID string, source io.Reader, size int64) error {
	if size <= 0 || size > MaximumSaveUploadSize {
		return errors.New("SAVE-CARD-SIZE-UNSUPPORTED")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return os.ErrClosed
	}
	card := store.objects[objectID]
	if card == nil {
		return errors.New("SAVE-CARD-MISSING")
	}
	if size != card.spec.CardSize {
		return errors.New("SAVE-CARD-SIZE-UNSUPPORTED")
	}
	temp, err := os.OpenFile(filepath.Join(card.directory, ".upload.tmp"),
		os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(temp, hash), source, size)
	if copyErr == nil && written != size {
		copyErr = io.ErrShortWrite
	}
	if copyErr == nil {
		var extra [1]byte
		if count, _ := source.Read(extra[:]); count != 0 {
			copyErr = errors.New("upload exceeds declared size")
		}
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	if closeErr := temp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if len(card.dirty) > 0 {
		if err = store.flushCard(card); err != nil {
			return err
		}
	}
	if _, err = store.backupCard(card, "pre-upload"); err != nil {
		return err
	}
	return store.activateCandidate(card, tempPath, checksum)
}

func (store *SaveStore) UploadStream(objectID string, source io.Reader) error {
	store.mu.RLock()
	if store.closed {
		store.mu.RUnlock()
		return os.ErrClosed
	}
	card := store.objects[objectID]
	if card == nil {
		store.mu.RUnlock()
		return errors.New("SAVE-CARD-MISSING")
	}
	size := card.spec.CardSize
	store.mu.RUnlock()
	return store.Upload(objectID, source, size)
}

func (store *SaveStore) activateCandidate(card *cardObject, sourcePath, checksum string) error {
	stagePath := filepath.Join(card.directory, ".restore.tmp")
	if err := os.Remove(stagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	stage, err := os.OpenFile(stagePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		_, err = io.CopyN(stage, source, card.spec.CardSize)
	}
	source.Close()
	if err == nil {
		err = stage.Sync()
	}
	if stage != nil {
		if closeErr := stage.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		os.Remove(stagePath)
		return err
	}
	if err = validateCandidate(stagePath, card.spec.CardSize, checksum); err != nil {
		os.Remove(stagePath)
		return err
	}
	previousMetadata := card.metadata
	if err = preparePrevious(card); err != nil {
		os.Remove(stagePath)
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = restorePrevious(card, previousMetadata)
		}
	}()
	if err = card.file.Close(); err != nil {
		return err
	}
	if err = os.Rename(stagePath, card.activePath); err != nil {
		card.file, _ = os.Open(card.activePath)
		return err
	}
	if err = syncDirectory(card.directory); err != nil {
		return err
	}
	card.file, err = os.Open(card.activePath)
	if err != nil {
		return err
	}
	info, err := card.file.Stat()
	if err != nil {
		return err
	}
	card.metadata.SHA256, card.metadata.UpdatedAt = checksum, time.Now().UTC()
	card.metadata.LastFlush, card.metadata.Dirty = time.Now().UTC(), false
	card.metadata.RecoveryState = "clean"
	setFileIdentity(&card.metadata, info)
	if err = writeSaveMetadata(card.metadataPath, card.metadata); err != nil {
		return err
	}
	reopened, err := hashRegularFile(card.activePath, card.spec.CardSize)
	if err != nil || reopened != checksum {
		return errors.New("activated card verification failed")
	}
	if err = commitPrevious(card.directory); err != nil {
		return err
	}
	committed = true
	return nil
}

func (store *SaveStore) OpenDownload(objectID, backupName string) (*os.File, string, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	card := store.objects[objectID]
	if card == nil {
		return nil, "", errors.New("SAVE-CARD-MISSING")
	}
	path, filename := card.activePath, safeDownloadName(card.spec)
	if backupName != "" {
		if filepath.Base(backupName) != backupName || !strings.HasSuffix(backupName, ".raw") ||
			strings.HasPrefix(backupName, ".") {
			return nil, "", errors.New("unsafe backup name")
		}
		path, filename = filepath.Join(card.directory, "backups", backupName), backupName
		metadata, metadataErr := loadBackupMetadata(
			strings.TrimSuffix(path, ".raw") + ".json")
		if metadataErr != nil || metadata.Name != backupName ||
			!backupMatches(metadata, card.spec) ||
			validateCandidate(path, card.spec.CardSize, metadata.SHA256) != nil {
			return nil, "", errors.New("SAVE-CARD-INVALID")
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != card.spec.CardSize {
		return nil, "", errors.New("SAVE-CARD-INVALID")
	}
	file, err := os.Open(path)
	return file, filename, err
}

func (store *SaveStore) ListBackups(objectID string) ([]BackupMetadata, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	card := store.objects[objectID]
	if card == nil {
		return nil, errors.New("SAVE-CARD-MISSING")
	}
	return listBackupMetadata(card.directory, card.spec)
}

func saveObjectDirectory(root string, object SaveObject) (string, error) {
	if !saveObjectPattern.MatchString(object.ID) || !SupportedSaveCardSize(object.CardSize) {
		return "", errors.New("SAVE-CARD-INVALID")
	}
	var relative string
	switch object.Mode {
	case MemoryCardEmulatedIndividual:
		if object.ID != "individual:"+object.GameID || !validID.MatchString(object.GameID) ||
			object.SharedCardName != "" {
			return "", errors.New("SAVE-CARD-INVALID: individual association")
		}
		relative = filepath.Join("individual", object.GameID)
	case MemoryCardEmulatedShared:
		if object.ID != "shared:"+object.SharedCardName ||
			!sharedCardPattern.MatchString(object.SharedCardName) || object.GameID != "" {
			return "", errors.New("SAVE-CARD-INVALID: shared association")
		}
		relative = filepath.Join("shared", object.SharedCardName)
	default:
		return "", errors.New("SAVE-CARD-INVALID: mode")
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(rootAbsolute, relative)
	if !strings.HasPrefix(directory, rootAbsolute+string(os.PathSeparator)) {
		return "", errors.New("SAVE-CARD-INVALID: managed path")
	}
	if err = validateManagedDirectoryTree(rootAbsolute, directory); err != nil {
		return "", err
	}
	return directory, nil
}

func validateManagedDirectoryTree(root, target string) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if targetAbsolute != rootAbsolute &&
		!strings.HasPrefix(targetAbsolute, rootAbsolute+string(os.PathSeparator)) {
		return errors.New("SAVE-CARD-INVALID: managed path")
	}
	parent := filepath.Dir(rootAbsolute)
	if err = safeDirectory(parent); err != nil {
		return errors.New("SAVE-CARD-INVALID: managed parent")
	}
	relative, err := filepath.Rel(parent, targetAbsolute)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return errors.New("SAVE-CARD-INVALID: managed path")
	}
	current := parent
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SAVE-CARD-INVALID: managed directory")
		}
	}
	return nil
}

func ensureManagedDirectoryTree(root, target string) error {
	if err := validateManagedDirectoryTree(root, target); err != nil {
		return err
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	parent := filepath.Dir(rootAbsolute)
	relative, err := filepath.Rel(parent, targetAbsolute)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return errors.New("SAVE-CARD-INVALID: managed path")
	}
	current := parent
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if statErr = os.Mkdir(current, 0o700); statErr != nil {
				return statErr
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SAVE-CARD-INVALID: managed directory")
		}
	}
	return nil
}

func validateCardFiles(directory string, object SaveObject) (SaveMetadata, error) {
	if err := safeDirectory(directory); err != nil {
		return SaveMetadata{}, fmt.Errorf("SAVE-CARD-MISSING: %w", err)
	}
	active := filepath.Join(directory, "active.raw")
	info, err := os.Lstat(active)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != object.CardSize {
		return SaveMetadata{}, errors.New("SAVE-CARD-INVALID: active card")
	}
	data, err := readRegularBounded(filepath.Join(directory, "metadata.json"), 64<<10)
	if err != nil {
		return SaveMetadata{}, errors.New("SAVE-CARD-INVALID: metadata")
	}
	var metadata SaveMetadata
	if err = json.Unmarshal(data, &metadata); err != nil ||
		metadata.FormatVersion != SaveOverlayFormatVersion ||
		metadata.Mode != object.Mode || metadata.GameID != object.GameID ||
		metadata.SharedCardName != object.SharedCardName ||
		metadata.CardSize != object.CardSize || len(metadata.SHA256) != 64 {
		return SaveMetadata{}, errors.New("SAVE-CARD-INVALID: metadata association")
	}
	if metadata.SizeIdentityMatches(info) {
		return metadata, nil
	}
	sum, err := hashRegularFile(active, object.CardSize)
	if err != nil || sum != metadata.SHA256 {
		return SaveMetadata{}, errors.New("SAVE-RECOVERY-AMBIGUOUS: active checksum")
	}
	setFileIdentity(&metadata, info)
	if err = writeSaveMetadata(filepath.Join(directory, "metadata.json"), metadata); err != nil {
		return SaveMetadata{}, err
	}
	return metadata, nil
}

func (metadata SaveMetadata) SizeIdentityMatches(info os.FileInfo) bool {
	if info.Size() != metadata.CardSize ||
		info.ModTime().UnixNano() != metadata.ModTimeUnixNano {
		return false
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev) == metadata.Device && stat.Ino == metadata.Inode
	}
	return true
}

func setFileIdentity(metadata *SaveMetadata, info os.FileInfo) {
	metadata.ModTimeUnixNano = info.ModTime().UnixNano()
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		metadata.Device, metadata.Inode = uint64(stat.Dev), stat.Ino
	}
}

func writeSaveMetadata(path string, metadata SaveMetadata) error {
	metadata.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'))
}

func atomicWrite(path string, data []byte) error {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".managed-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func hashRegularFile(path string, expected int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != expected {
		return "", errors.New("invalid regular card file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.CopyN(hash, file, expected)
	if err != nil || written != expected {
		return "", errors.New("card checksum read failed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func zeroCardHash(size int64) string {
	hash := sha256.New()
	zero := make([]byte, 64<<10)
	for remaining := size; remaining > 0; {
		count := min(remaining, int64(len(zero)))
		_, _ = hash.Write(zero[:count])
		remaining -= count
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func readRegularBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("invalid managed file")
	}
	return os.ReadFile(path)
}

func safeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid managed directory")
	}
	return nil
}

func rejectCheckpointConflicts(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var candidates []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".checkpoint") ||
			strings.HasPrefix(entry.Name(), ".restore") ||
			strings.HasPrefix(entry.Name(), ".upload") {
			candidates = append(candidates, entry.Name())
		}
	}
	if len(candidates) > 1 {
		return errors.New("SAVE-RECOVERY-AMBIGUOUS: conflicting staged cards")
	}
	for _, candidate := range candidates {
		info, statErr := os.Lstat(filepath.Join(directory, candidate))
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SAVE-RECOVERY-AMBIGUOUS: invalid staged card")
		}
		if err = os.Remove(filepath.Join(directory, candidate)); err != nil {
			return err
		}
	}
	return nil
}

func previousPath(directory string) string {
	return filepath.Join(directory, ".previous-confirmed.raw")
}

func previousMetadataPath(directory string) string {
	return filepath.Join(directory, ".previous-confirmed.json")
}

func preparePrevious(card *cardObject) error {
	path := previousPath(card.directory)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(previousMetadataPath(card.directory)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(card.metadata, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(
		previousMetadataPath(card.directory), append(data, '\n')); err != nil {
		return err
	}
	if err := os.Link(card.activePath, path); err != nil {
		if copyErr := copyValidatedCandidate(
			card.activePath, path, card.spec.CardSize, card.metadata.SHA256); copyErr != nil {
			return copyErr
		}
	}
	return syncDirectory(card.directory)
}

func commitPrevious(directory string) error {
	if err := os.Remove(previousMetadataPath(directory)); err != nil {
		return err
	}
	if err := os.Remove(previousPath(directory)); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func restorePrevious(card *cardObject, metadata SaveMetadata) error {
	path := previousPath(card.directory)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != card.spec.CardSize {
		return errors.New("SAVE-RECOVERY-AMBIGUOUS: previous checkpoint unavailable")
	}
	if card.file != nil {
		_ = card.file.Close()
	}
	if err = os.Rename(path, card.activePath); err != nil {
		return err
	}
	if err = syncDirectory(card.directory); err != nil {
		return err
	}
	card.file, err = os.Open(card.activePath)
	if err != nil {
		return err
	}
	card.metadata = metadata
	if err = writeSaveMetadata(card.metadataPath, metadata); err != nil {
		return err
	}
	if err = os.Remove(previousMetadataPath(card.directory)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(card.directory)
}

func recoverInterruptedActivation(directory string, spec SaveObject) error {
	path := previousPath(directory)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if removeErr := os.Remove(previousMetadataPath(directory)); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != spec.CardSize {
		return errors.New("SAVE-RECOVERY-AMBIGUOUS: invalid previous checkpoint")
	}
	var metadata SaveMetadata
	data, metadataErr := readRegularBounded(previousMetadataPath(directory), 64<<10)
	if metadataErr == nil {
		metadataErr = json.Unmarshal(data, &metadata)
	}
	if metadataErr != nil {
		// A crash after the previous-metadata marker was removed but before
		// its raw card was unlinked is deterministically rolled back. Rebuild
		// safe association metadata from the managed object and raw checksum.
		current, currentErr := readRegularBounded(
			filepath.Join(directory, "metadata.json"), 64<<10)
		if currentErr != nil || json.Unmarshal(current, &metadata) != nil {
			return errors.New("SAVE-RECOVERY-AMBIGUOUS: metadata unavailable")
		}
		metadata.Mode, metadata.GameID = spec.Mode, spec.GameID
		metadata.SharedCardName, metadata.CardSize = spec.SharedCardName, spec.CardSize
	}
	if metadata.Mode != spec.Mode || metadata.GameID != spec.GameID ||
		metadata.SharedCardName != spec.SharedCardName ||
		metadata.CardSize != spec.CardSize {
		return errors.New("SAVE-RECOVERY-AMBIGUOUS: previous association")
	}
	sum, hashErr := hashRegularFile(path, spec.CardSize)
	if hashErr != nil {
		return errors.New("SAVE-RECOVERY-AMBIGUOUS: previous checksum")
	}
	if len(metadata.SHA256) == 64 && metadataErr == nil && sum != metadata.SHA256 {
		return errors.New("SAVE-RECOVERY-AMBIGUOUS: previous checksum")
	}
	metadata.SHA256 = sum
	if err = os.Rename(path, filepath.Join(directory, "active.raw")); err != nil {
		return err
	}
	info, err = os.Lstat(filepath.Join(directory, "active.raw"))
	if err != nil {
		return err
	}
	setFileIdentity(&metadata, info)
	metadata.Dirty, metadata.RecoveryState = false, "rolled-back-interrupted-checkpoint"
	if err = writeSaveMetadata(filepath.Join(directory, "metadata.json"), metadata); err != nil {
		return err
	}
	if err = os.Remove(previousMetadataPath(directory)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range []string{
		".checkpoint.tmp", ".restore.tmp", ".restore-selected.tmp", ".upload.tmp",
	} {
		candidate := filepath.Join(directory, name)
		candidateInfo, candidateErr := os.Lstat(candidate)
		if errors.Is(candidateErr, os.ErrNotExist) {
			continue
		}
		if candidateErr != nil || !candidateInfo.Mode().IsRegular() ||
			candidateInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("SAVE-RECOVERY-AMBIGUOUS: invalid staged card")
		}
		if err = os.Remove(candidate); err != nil {
			return err
		}
	}
	return syncDirectory(directory)
}

func listBackupMetadata(directory string, object SaveObject) ([]BackupMetadata, error) {
	backupDirectory := filepath.Join(directory, "backups")
	if err := safeDirectory(backupDirectory); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(backupDirectory)
	if err != nil {
		return nil, err
	}
	var result []BackupMetadata
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			!strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		metadata, loadErr := loadBackupMetadata(filepath.Join(backupDirectory, entry.Name()))
		if loadErr != nil || !backupMatches(metadata, object) {
			continue
		}
		rawPath := filepath.Join(backupDirectory, metadata.Name)
		info, statErr := os.Lstat(rawPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Size() != object.CardSize {
			continue
		}
		result = append(result, metadata)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func loadBackupMetadata(path string) (BackupMetadata, error) {
	data, err := readRegularBounded(path, 64<<10)
	if err != nil {
		return BackupMetadata{}, err
	}
	var metadata BackupMetadata
	if err = json.Unmarshal(data, &metadata); err != nil ||
		metadata.FormatVersion != SaveOverlayFormatVersion ||
		filepath.Base(metadata.Name) != metadata.Name ||
		!strings.HasSuffix(metadata.Name, ".raw") ||
		len(metadata.SHA256) != 64 || !SupportedSaveCardSize(metadata.CardSize) {
		return BackupMetadata{}, errors.New("invalid backup metadata")
	}
	return metadata, nil
}

func backupMatches(metadata BackupMetadata, object SaveObject) bool {
	return metadata.Mode == object.Mode && metadata.GameID == object.GameID &&
		metadata.SharedCardName == object.SharedCardName &&
		metadata.CardSize == object.CardSize
}

func validateCandidate(path string, size int64, checksum string) error {
	sum, err := hashRegularFile(path, size)
	if err != nil {
		return err
	}
	if sum != checksum {
		return errors.New("card checksum mismatch")
	}
	return nil
}

func pruneBackups(directory string, maximum int) error {
	if maximum < 1 {
		maximum = 1
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var bases []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			!strings.HasSuffix(entry.Name(), ".raw") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		bases = append(bases, strings.TrimSuffix(entry.Name(), ".raw"))
	}
	sort.Strings(bases)
	for len(bases) > maximum {
		base := bases[0]
		rawPath, metadataPath := filepath.Join(directory, base+".raw"),
			filepath.Join(directory, base+".json")
		for _, path := range []string{rawPath, metadataPath} {
			info, statErr := os.Lstat(path)
			if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("SAVE-BACKUP-FAILED: unsafe backup cleanup target")
			}
		}
		if err = os.Remove(rawPath); err != nil {
			return err
		}
		if err = os.Remove(metadataPath); err != nil {
			return err
		}
		bases = bases[1:]
	}
	return syncDirectory(directory)
}

func safeDownloadName(object SaveObject) string {
	if object.GameID != "" {
		return "wiibridge-" + object.GameID + ".raw"
	}
	return "wiibridge-shared-" + object.SharedCardName + ".raw"
}

func (store *SaveStore) totalDirtyBlocks() int {
	total := 0
	for _, card := range store.objects {
		total += len(card.dirty)
	}
	return total
}

func (store *SaveStore) totalJournalBytes() int64 {
	var total int64
	for _, card := range store.objects {
		total += card.journalBytes
	}
	return total
}
