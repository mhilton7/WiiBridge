// Package vdisk synthesizes immutable MBR/FAT32 metadata and maps data clusters
// directly to read-only source ranges.
package vdisk

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"wiibridge/server/host-daemon/scanner"
	"wiibridge/shared/model"
	"wiibridge/shared/perf"
)

const (
	sectorSize      = int64(512)
	partitionStart  = int64(2048)
	reservedSectors = int64(32)
	numFATs         = int64(2)
	// Match USB Loader GX r1283's FAT32 split boundary: one maximum WiiBridge
	// cluster below 4 GiB. A 4 GiB-minus-4 KiB file rounds up to exactly 2^32
	// bytes on a 32 KiB-cluster volume and overflows 32-bit FAT chain-length
	// accounting even though the directory entry's file size still fits.
	maxSegment = int64(4<<30) - int64(32<<10)

	// FAT32 cluster numbers 0x0ffffff0 through 0x0ffffff6 are reserved.
	// Because data clusters begin at number 2, this is the largest safe count
	// whose final cluster number remains below the reserved range.
	maxFAT32DataClusters = int64(0x0fffffee)

	// A classic MBR partition length is a uint32 sector count, while a 32-bit
	// LBA disk can contain sectors 0 through 0xffffffff.
	maxMBRPartitionSectors = int64(1<<32 - 1)
	maxLBA32DiskSectors    = int64(1 << 32)

	// USB Loader GX and the Wii FAT stack conventionally use 32 KiB clusters
	// for large FAT32 disks. This also keeps the FAT substantially smaller on
	// terabyte libraries than the minimum mathematically valid cluster size.
	largeWiiVolumeThreshold = int64(32 << 30)

	// USB Loader GX's bundled libfat treats both zero and 0xffffffff FSInfo
	// free-cluster counts as a request to walk the complete FAT at mount time.
	// Always retain at least one real free cluster so the synthetic disk can
	// publish an exact, non-sentinel count and keep mounting proportional to
	// directory metadata rather than virtual disk capacity.
	minFreeClusters = int64(1)
)

var wiiSectorsPerCluster = [...]int64{8, 16, 32, 64}

type extent struct {
	start, length int64
	source        *model.Source
	sourceOffset  int64
	zero          bool
}

type clusterChain struct {
	first, count uint32
}

type Disk struct {
	size              int64
	sectorsPerCluster int64
	metadata          map[int64][]byte
	fatStart          int64
	fatSectors        int64
	fatChains         []clusterChain
	extents           []extent
	snapshot          model.Snapshot
	metrics           *perf.Registry
	onSourceFailure   func(string)
	lastSourceFailure atomic.Int64
}

type diskGeometry struct {
	sectorsPerCluster int64
	clusterSize       int64
	wbfsDirClusters   int64
	allocatedClusters int64
	freeClusters      int64
	totalClusters     int64
	fatSectors        int64
	firstData         int64
	partSectors       int64
	diskSectors       int64
}

func clustersForBytes(size, clusterSize int64) int64 {
	if size <= 0 {
		return 0
	}
	return 1 + (size-1)/clusterSize
}

func selectDiskGeometry(fileLengths []int64, wbfsEntries int64) (diskGeometry, error) {
	candidates := wiiSectorsPerCluster[:]
	var logicalBytes int64
	for _, length := range fileLengths {
		if length < 0 {
			return diskGeometry{}, errors.New("Wii library contains a negative file size")
		}
		if length >= largeWiiVolumeThreshold-logicalBytes {
			// Preserve the proven 4 KiB layout for smaller libraries, but use
			// the Wii-compatible 32 KiB layout once payload size reaches the
			// conventional large-volume threshold.
			candidates = wiiSectorsPerCluster[len(wiiSectorsPerCluster)-1:]
			break
		}
		logicalBytes += length
	}
	for _, sectorsPerCluster := range candidates {
		clusterSize := sectorSize * sectorsPerCluster
		var dataClusters int64
		for _, length := range fileLengths {
			if dataClusters > maxFAT32DataClusters-clustersForBytes(length, clusterSize) {
				dataClusters = maxFAT32DataClusters + 1
				break
			}
			dataClusters += clustersForBytes(length, clusterSize)
		}
		wbfsDirClusters := clustersForBytes(wbfsEntries*32, clusterSize)
		if wbfsDirClusters < 1 {
			wbfsDirClusters = 1
		}
		const rootClusters = int64(1)
		allocatedClusters := rootClusters + wbfsDirClusters + dataClusters
		totalClusters := allocatedClusters + minFreeClusters
		// FAT32 requires at least 65,525 clusters. Unallocated clusters are
		// virtual zero space and do not consume persistent storage.
		if totalClusters < 65525 {
			totalClusters = 65525
		}
		if totalClusters > maxFAT32DataClusters {
			continue
		}
		fatSectors := ((totalClusters+2)*4 + sectorSize - 1) / sectorSize
		firstData := reservedSectors + numFATs*fatSectors
		partSectors := firstData + totalClusters*sectorsPerCluster
		diskSectors := partitionStart + partSectors
		if partSectors > maxMBRPartitionSectors || diskSectors > maxLBA32DiskSectors {
			continue
		}
		return diskGeometry{
			sectorsPerCluster: sectorsPerCluster,
			clusterSize:       clusterSize,
			wbfsDirClusters:   wbfsDirClusters,
			allocatedClusters: allocatedClusters,
			freeClusters:      totalClusters - allocatedClusters,
			totalClusters:     totalClusters,
			fatSectors:        fatSectors,
			firstData:         firstData,
			partSectors:       partSectors,
			diskSectors:       diskSectors,
		}, nil
	}
	return diskGeometry{}, errors.New("Wii library exceeds FAT32/MBR capacity")
}

func Build(catalog string, games []model.Game, appVersion string) (*Disk, error) {
	if catalog == "" {
		return nil, errors.New("catalog ID required")
	}
	games = append([]model.Game(nil), games...)
	sort.Slice(games, func(i, j int) bool { return games[i].ID < games[j].ID })
	type virtualFile struct {
		name, short string
		game        model.Game
		off, length int64
		first       uint32
	}
	var files []virtualFile
	for _, g := range games {
		for off, seg := int64(0), 0; off < g.Size; seg++ {
			n := g.Size - off
			if n > maxSegment {
				n = maxSegment
			}
			ext := "WBFS"
			if seg > 0 {
				ext = fmt.Sprintf("WBF%d", seg)
			}
			longName := fmt.Sprintf("%s.%s", g.ID, strings.ToLower(ext))
			sum := sha256.Sum256([]byte(longName))
			short := fmt.Sprintf("%-8s%-3s", fmt.Sprintf("%.2s%05X", g.ID, sum[:3]), "WBF")
			files = append(files, virtualFile{name: longName, short: short, game: g, off: off, length: n})
			off += n
		}
	}
	// Each payload uses one VFAT long-name entry plus one unique 8.3 alias.
	wbfsEntries := int64(2 + 2*len(files))
	fileLengths := make([]int64, len(files))
	for index := range files {
		fileLengths[index] = files[index].length
	}
	geometry, err := selectDiskGeometry(fileLengths, wbfsEntries)
	if err != nil {
		return nil, err
	}
	sectorsPerCluster := geometry.sectorsPerCluster
	clusterSize := geometry.clusterSize
	wbfsDirClusters := geometry.wbfsDirClusters
	const rootClusters = int64(1)
	fatSectors := geometry.fatSectors
	firstData := geometry.firstData
	partSectors := geometry.partSectors
	diskSectors := geometry.diskSectors
	signatureHash := sha256.New()
	signatureHash.Write([]byte(catalog))
	for _, file := range files {
		fmt.Fprintf(signatureHash, "\x00%s\x00%d\x00%d", file.name, file.off, file.length)
	}
	signatureBytes := signatureHash.Sum(nil)
	diskSignature := binary.LittleEndian.Uint32(signatureBytes[:4])
	if diskSignature == 0 {
		// Zero means that an MBR disk has no identity. It is particularly
		// unsuitable for this fixed, read-only USB LUN because Windows cannot
		// assign and persist a replacement signature.
		diskSignature = 0x57424d53
	}
	d := &Disk{
		size: diskSectors * sectorSize, sectorsPerCluster: sectorsPerCluster,
		metadata: map[int64][]byte{},
	}
	d.metadata[0] = mbr(partitionStart, partSectors, diskSignature)
	boot := bootSector(partSectors, fatSectors, sectorsPerCluster, uint32(2), diskSignature)
	d.metadata[partitionStart] = boot
	d.metadata[partitionStart+6] = append([]byte(nil), boot...)
	firstFreeCluster := uint32(2 + geometry.allocatedClusters)
	info := fsInfo(uint32(geometry.freeClusters), firstFreeCluster)
	d.metadata[partitionStart+1] = info
	d.metadata[partitionStart+7] = append([]byte(nil), info...)
	nextCluster := uint32(2)
	chain := func(count int64) uint32 {
		first := nextCluster
		if count > 0 {
			d.fatChains = append(d.fatChains, clusterChain{
				first: first, count: uint32(count),
			})
		}
		nextCluster += uint32(count)
		return first
	}
	d.fatStart = partitionStart + reservedSectors
	d.fatSectors = fatSectors
	chain(rootClusters) // FAT32 root is fixed at cluster 2.
	wbfsCluster := chain(wbfsDirClusters)
	for i := range files {
		files[i].first = chain((files[i].length + clusterSize - 1) / clusterSize)
	}
	if nextCluster != firstFreeCluster {
		return nil, errors.New("internal Wii FAT32 allocation mismatch")
	}
	dataStartSector := partitionStart + firstData
	rootDir := make([]byte, rootClusters*clusterSize)
	write83(rootDir[0:32], "WIIBRIDGE", 0, 0, 0x08)
	write83(rootDir[32:64], "WBFS       ", wbfsCluster, 0, 0x10)
	dir := make([]byte, wbfsDirClusters*clusterSize)
	write83(dir[0:32], ".", wbfsCluster, 0, 0x10)
	write83(dir[32:64], "..", 0, 0, 0x10)
	for i, f := range files {
		base := (2 + i*2) * 32
		writeLFN(dir[base:base+32], f.name, lfnChecksum([]byte(f.short)))
		// USB Loader GX opens WBFS files with O_RDWR even when it only needs
		// to extract a banner or boot a game. A DOS read-only attribute makes
		// that open fail before any payload read. Immutability is enforced by
		// the read-only NBD export and USB LUN, not by the FAT entry.
		write83(dir[base+32:base+64], f.short, f.first, uint32(f.length), 0x20)
	}
	for i := int64(0); i < rootClusters*sectorsPerCluster; i++ {
		d.metadata[dataStartSector+i] = append([]byte(nil), rootDir[i*sectorSize:(i+1)*sectorSize]...)
	}
	wbfsStartSector := dataStartSector + rootClusters*sectorsPerCluster
	for i := int64(0); i < wbfsDirClusters*sectorsPerCluster; i++ {
		d.metadata[wbfsStartSector+i] = append([]byte(nil), dir[i*sectorSize:(i+1)*sectorSize]...)
	}
	cursor := wbfsStartSector + wbfsDirClusters*sectorsPerCluster
	for _, vf := range files {
		fileStart := cursor * sectorSize
		remaining := vf.length
		gameOffset := vf.off
		for remaining > 0 {
			source, sourceOff, err := locate(vf.game.Sources, gameOffset)
			if err != nil {
				return nil, err
			}
			n := remaining
			available := source.Length - sourceOff
			if n > available {
				n = available
			}
			d.extents = append(d.extents, extent{
				start: fileStart + gameOffset - vf.off, length: n, source: source, sourceOffset: sourceOff,
			})
			gameOffset += n
			remaining -= n
		}
		padding := ((vf.length+clusterSize-1)/clusterSize)*clusterSize - vf.length
		if padding > 0 {
			d.extents = append(d.extents, extent{start: fileStart + vf.length, length: padding, zero: true})
		}
		cursor += ((vf.length + clusterSize - 1) / clusterSize) * sectorsPerCluster
	}
	metaHash := d.metadataIdentity()
	var virtualFiles []model.VirtualFile
	for _, file := range files {
		virtualFiles = append(virtualFiles, model.VirtualFile{
			Path: "/wbfs/" + file.name, GameID: file.game.ID,
			LogicalStart: file.off, Length: file.length,
		})
	}
	identityBytes, err := json.Marshal(struct {
		Games []model.Game
		Files []model.VirtualFile
	}{games, virtualFiles})
	if err != nil {
		return nil, err
	}
	idMaterial := append([]byte(catalog+"\x00"+metaHash+"\x00"), identityBytes...)
	idSum := sha256.Sum256(idMaterial)
	d.snapshot = model.Snapshot{
		SnapshotID: hex.EncodeToString(idSum[:16]), CatalogID: catalog,
		VirtualDiskSize: d.size, MetadataHash: metaHash, Application: appVersion,
		Created: time.Now().UTC(), Games: games, VirtualFiles: virtualFiles,
	}
	return d, nil
}

func locate(sources []model.Source, logical int64) (*model.Source, int64, error) {
	for i := range sources {
		if logical >= sources[i].Offset && logical < sources[i].Offset+sources[i].Length {
			return &sources[i], logical - sources[i].Offset, nil
		}
	}
	return nil, 0, errors.New("source extent gap")
}

func (d *Disk) Size() int64              { return d.size }
func (d *Disk) Snapshot() model.Snapshot { return d.snapshot }

func (d *Disk) SetObserver(metrics *perf.Registry, sourceFailure func(string)) {
	d.metrics, d.onSourceFailure = metrics, sourceFailure
}

func (d *Disk) metricsEnabled() bool {
	return d.metrics != nil && d.metrics.Enabled()
}

// metadataIdentity fingerprints the compact description used to synthesize the
// disk. Hashing every apparent FAT sector would make Host startup proportional
// to virtual library capacity even though those sectors are generated on
// demand and consume no resident storage.
func (d *Disk) metadataIdentity() string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("wiibridge-vdisk-compact-metadata-v1\x00"))
	var number [8]byte
	writeInt64 := func(value int64) {
		binary.LittleEndian.PutUint64(number[:], uint64(value))
		_, _ = hash.Write(number[:])
	}
	writeUint32 := func(value uint32) {
		binary.LittleEndian.PutUint32(number[:4], value)
		_, _ = hash.Write(number[:4])
	}
	writeInt64(d.size)
	writeInt64(sectorSize)
	writeInt64(d.sectorsPerCluster)
	writeInt64(d.fatStart)
	writeInt64(d.fatSectors)
	writeInt64(numFATs)

	keys := make([]int64, 0, len(d.metadata))
	for sector := range d.metadata {
		keys = append(keys, sector)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	writeInt64(int64(len(keys)))
	for _, sector := range keys {
		data := d.metadata[sector]
		writeInt64(sector)
		writeInt64(int64(len(data)))
		_, _ = hash.Write(data)
	}

	writeInt64(int64(len(d.fatChains)))
	for _, chain := range d.fatChains {
		writeUint32(chain.first)
		writeUint32(chain.count)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (d *Disk) fatSectorIndex(sector int64) (int64, bool) {
	relative := sector - d.fatStart
	if relative >= 0 && relative < d.fatSectors {
		return relative, true
	}
	relative -= d.fatSectors
	return relative, relative >= 0 && relative < d.fatSectors
}

func (d *Disk) fatValue(cluster uint32) uint32 {
	switch cluster {
	case 0:
		return 0x0ffffff8
	case 1:
		return 0xffffffff
	}
	index := sort.Search(len(d.fatChains), func(index int) bool {
		chain := d.fatChains[index]
		return chain.first+chain.count > cluster
	})
	if index == len(d.fatChains) {
		return 0
	}
	chain := d.fatChains[index]
	if cluster < chain.first {
		return 0
	}
	if cluster+1 == chain.first+chain.count {
		return 0x0fffffff
	}
	return cluster + 1
}

func (d *Disk) fillFATSector(target []byte, fatSector int64) {
	clear(target)
	firstCluster := uint32(fatSector * (sectorSize / 4))
	lastCluster := firstCluster + uint32(len(target)/4)
	if firstCluster == 0 {
		binary.LittleEndian.PutUint32(target[0:4], 0x0ffffff8)
		binary.LittleEndian.PutUint32(target[4:8], 0xffffffff)
	}
	// Chains are immutable and ordered. Search once for the sector, then walk
	// only its intersecting chains instead of searching for every FAT entry.
	index := sort.Search(len(d.fatChains), func(index int) bool {
		chain := d.fatChains[index]
		return chain.first+chain.count > firstCluster
	})
	for ; index < len(d.fatChains); index++ {
		chain := d.fatChains[index]
		if chain.first >= lastCluster {
			break
		}
		end := chain.first + chain.count
		for cluster := max(firstCluster, chain.first, uint32(2)); cluster < min(lastCluster, end); cluster++ {
			value := cluster + 1
			if cluster+1 == end {
				value = 0x0fffffff
			}
			offset := int(cluster-firstCluster) * 4
			binary.LittleEndian.PutUint32(target[offset:offset+4], value)
		}
	}
}

func (d *Disk) metadataSector(sector int64) ([]byte, bool) {
	if data, ok := d.metadata[sector]; ok {
		return data, true
	}
	fatSector, ok := d.fatSectorIndex(sector)
	if !ok {
		return nil, false
	}
	data := make([]byte, sectorSize)
	d.fillFATSector(data, fatSector)
	return data, true
}

func (d *Disk) forEachMetadataSector(visit func(int64, []byte) error) error {
	type region struct {
		start, count int64
		fat          bool
		data         []byte
	}
	regions := make([]region, 0, len(d.metadata)+int(numFATs))
	for sector, data := range d.metadata {
		regions = append(regions, region{start: sector, count: 1, data: data})
	}
	for copyNumber := int64(0); copyNumber < numFATs; copyNumber++ {
		regions = append(regions, region{
			start: d.fatStart + copyNumber*d.fatSectors,
			count: d.fatSectors, fat: true,
		})
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].start < regions[j].start })
	buffer := make([]byte, sectorSize)
	for _, item := range regions {
		for index := int64(0); index < item.count; index++ {
			data := item.data
			if item.fat {
				d.fillFATSector(buffer, index)
				data = buffer
			}
			if err := visit(item.start+index, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Disk) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || int64(len(p)) > d.size-off {
		return 0, io.EOF
	}
	var scratch [sectorSize]byte
	for done := 0; done < len(p); {
		pos := off + int64(done)
		sector := pos / sectorSize
		inSector := pos % sectorSize
		n := len(p) - done
		if data, ok := d.metadata[sector]; ok {
			if max := int(sectorSize - inSector); n > max {
				n = max
			}
			copy(p[done:done+n], data[inSector:inSector+int64(n)])
			if d.metricsEnabled() {
				d.metrics.Disk.MetadataReads.Add(1)
			}
		} else if fatSector, ok := d.fatSectorIndex(sector); ok {
			n = min(n, int(sectorSize-inSector))
			if inSector == 0 && n == int(sectorSize) {
				d.fillFATSector(p[done:done+n], fatSector)
			} else {
				d.fillFATSector(scratch[:], fatSector)
				copy(p[done:done+n], scratch[inSector:inSector+int64(n)])
			}
			if d.metricsEnabled() {
				d.metrics.Disk.MetadataReads.Add(1)
			}
		} else {
			var mapped *extent
			// Build appends disjoint extents in virtual-offset order. Taking
			// the slice element's address also avoids escaping a range-variable
			// copy for every extent examined on every read.
			index := sort.Search(len(d.extents), func(index int) bool {
				e := &d.extents[index]
				return e.start+e.length > pos
			})
			if index < len(d.extents) && d.extents[index].start <= pos {
				mapped = &d.extents[index]
			}
			if mapped == nil {
				// Metadata occupies whole sectors, so an unmapped gap can be
				// cleared through the current sector without hiding metadata.
				if max := int(sectorSize - inSector); n > max {
					n = max
				}
				clear(p[done : done+n])
				if d.metricsEnabled() {
					d.metrics.Disk.ZeroReads.Add(1)
				}
			} else {
				if available := mapped.start + mapped.length - pos; int64(n) > available {
					n = int(available)
				}
				if mapped.zero {
					clear(p[done : done+n])
					if d.metricsEnabled() {
						d.metrics.Disk.ZeroReads.Add(1)
					}
				} else {
					started := time.Time{}
					if d.metricsEnabled() {
						started = time.Now()
					}
					if err := scanner.VerifySource(*mapped.source); err != nil {
						if d.metricsEnabled() {
							d.metrics.Source.IdentityErrors.Add(1)
							d.metrics.ObserveSourceRead(0, time.Since(started), err)
						}
						code := vdiskSourceFailureCode(err)
						if !errors.Is(err, os.ErrNotExist) &&
							!errors.Is(err, os.ErrPermission) {
							code = "SOURCE-IDENTITY-CHANGED"
						}
						d.reportSourceFailure(code)
						return done, err
					}
					f, err := os.Open(mapped.source.Path)
					if err != nil {
						if d.metricsEnabled() {
							d.metrics.ObserveSourceRead(0, time.Since(started), err)
						}
						d.reportSourceFailure(vdiskSourceFailureCode(err))
						return done, err
					}
					if d.metricsEnabled() {
						d.metrics.Source.OpenHandles.Add(1)
					}
					readN, readErr := readAtFull(f, p[done:done+n],
						mapped.sourceOffset+pos-mapped.start)
					closeErr := f.Close()
					if d.metricsEnabled() {
						d.metrics.Source.OpenHandles.Add(-1)
						d.metrics.Disk.PayloadReads.Add(1)
						d.metrics.ObserveSourceRead(readN, time.Since(started), readErr)
					}
					if readErr != nil {
						d.reportSourceFailure(vdiskSourceFailureCode(readErr))
						return done + readN, readErr
					}
					if closeErr != nil {
						return done + readN, closeErr
					}
				}
			}
		}
		done += n
	}
	return len(p), nil
}

func (d *Disk) reportSourceFailure(code string) {
	if d.onSourceFailure == nil {
		return
	}
	now := time.Now().UnixNano()
	last := d.lastSourceFailure.Load()
	if now-last < int64(10*time.Second) ||
		!d.lastSourceFailure.CompareAndSwap(last, now) {
		return
	}
	d.onSourceFailure(code)
}

func vdiskSourceFailureCode(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "SOURCE-PERMISSION-DENIED"
	}
	return "SOURCE-READ-FAILED"
}

func readAtFull(r io.ReaderAt, p []byte, off int64) (int, error) {
	done := 0
	for done < len(p) {
		n, err := r.ReadAt(p[done:], off+int64(done))
		done += n
		if done == len(p) {
			return done, nil
		}
		if err != nil {
			return done, err
		}
		if n == 0 {
			return done, io.ErrNoProgress
		}
	}
	return done, nil
}

func mbr(start, sectors int64, signature uint32) []byte {
	b := make([]byte, 512)
	binary.LittleEndian.PutUint32(b[440:444], signature)
	p := b[446:462]
	p[0], p[4] = 0x00, 0x0c
	p[1], p[2], p[3] = 0x20, 0x21, 0x00
	p[5], p[6], p[7] = 0xfe, 0xff, 0xff
	binary.LittleEndian.PutUint32(p[8:12], uint32(start))
	binary.LittleEndian.PutUint32(p[12:16], uint32(sectors))
	b[510], b[511] = 0x55, 0xaa
	return b
}

func bootSector(sectors, fatSectors, sectorsPerCluster int64, root, volumeID uint32) []byte {
	b := make([]byte, 512)
	copy(b[0:3], []byte{0xeb, 0x58, 0x90})
	copy(b[3:11], "WIIBRDG ")
	binary.LittleEndian.PutUint16(b[11:13], 512)
	b[13] = byte(sectorsPerCluster)
	binary.LittleEndian.PutUint16(b[14:16], uint16(reservedSectors))
	b[16] = byte(numFATs)
	b[21] = 0xf8
	// Use conventional LBA-assisted disk geometry. Modern hosts address this
	// image by LBA, but Windows and mtools still reject a FAT BPB whose legacy
	// geometry and hidden-sector fields are left at zero.
	binary.LittleEndian.PutUint16(b[24:26], 63)
	binary.LittleEndian.PutUint16(b[26:28], 255)
	binary.LittleEndian.PutUint32(b[28:32], uint32(partitionStart))
	binary.LittleEndian.PutUint32(b[32:36], uint32(sectors))
	binary.LittleEndian.PutUint32(b[36:40], uint32(fatSectors))
	binary.LittleEndian.PutUint32(b[44:48], root)
	binary.LittleEndian.PutUint16(b[48:50], 1)
	binary.LittleEndian.PutUint16(b[50:52], 6)
	b[64], b[66] = 0x80, 0x29
	binary.LittleEndian.PutUint32(b[67:71], volumeID)
	copy(b[71:82], "WIIBRIDGE  ")
	copy(b[82:90], "FAT32   ")
	b[510], b[511] = 0x55, 0xaa
	return b
}

func fsInfo(freeClusters, nextFreeCluster uint32) []byte {
	b := make([]byte, 512)
	copy(b[0:4], []byte("RRaA"))
	copy(b[484:488], []byte("rrAa"))
	binary.LittleEndian.PutUint32(b[488:492], freeClusters)
	binary.LittleEndian.PutUint32(b[492:496], nextFreeCluster)
	b[510], b[511] = 0x55, 0xaa
	return b
}

func write83(dst []byte, name string, cluster uint32, size uint32, attr byte) {
	for i := range dst {
		dst[i] = 0
	}
	for i := 0; i < 11; i++ {
		dst[i] = ' '
	}
	if name == "." || name == ".." {
		copy(dst[:], name)
	} else {
		copy(dst[:11], strings.ToUpper(name))
	}
	dst[11] = attr
	binary.LittleEndian.PutUint16(dst[20:22], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(dst[26:28], uint16(cluster))
	binary.LittleEndian.PutUint32(dst[28:32], size)
}

func lfnChecksum(short []byte) byte {
	var sum byte
	for _, c := range short[:11] {
		sum = ((sum & 1) << 7) + (sum >> 1) + c
	}
	return sum
}

func writeLFN(dst []byte, name string, checksum byte) {
	for i := range dst {
		dst[i] = 0xff
	}
	dst[0] = 0x41 // final and first entry in this one-entry LFN
	dst[11] = 0x0f
	dst[12] = 0
	dst[13] = checksum
	dst[26], dst[27] = 0, 0
	positions := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
	runes := []rune(name)
	for i, pos := range positions {
		var value uint16 = 0xffff
		if i < len(runes) {
			value = uint16(runes[i])
		} else if i == len(runes) {
			value = 0
		}
		binary.LittleEndian.PutUint16(dst[pos:pos+2], value)
	}
}

func (d *Disk) MetadataBytes() []byte {
	var out bytes.Buffer
	_ = d.forEachMetadataSector(func(_ int64, data []byte) error {
		_, _ = out.Write(data)
		return nil
	})
	return out.Bytes()
}
