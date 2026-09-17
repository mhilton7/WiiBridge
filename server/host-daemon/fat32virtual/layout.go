// Package fat32virtual builds compact FAT32 metadata and maps file clusters to
// immutable source files. The apparent disk can be large, but payload bytes are
// never written to managed storage.
package fat32virtual

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"unicode/utf16"
)

const (
	SectorSize        = int64(512)
	PartitionStart    = int64(2048)
	reservedSectors   = int64(32)
	numberOfFATs      = int64(2)
	sectorsPerCluster = int64(64)
	ClusterSize       = SectorSize * sectorsPerCluster
	minimumClusters   = int64(65525)
)

type Identity struct {
	FilesystemID    string `json:"filesystem_id,omitempty"`
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"mtime_unix_nano"`
	Device          uint64 `json:"device"`
	Inode           uint64 `json:"inode"`
	SHA256          string `json:"sha256"`
}

type File struct {
	VirtualPath   string   `json:"virtual_path"`
	VirtualOffset int64    `json:"virtual_offset"`
	LogicalSize   int64    `json:"logical_length"`
	AllocatedSize int64    `json:"allocated_length"`
	SourcePath    string   `json:"source_path"`
	SourceOffset  int64    `json:"source_offset"`
	SourceSize    int64    `json:"source_size"`
	Identity      Identity `json:"source_fingerprint"`
	GameID        string   `json:"game_id"`
	Revision      byte     `json:"revision"`
	DiscNumber    byte     `json:"disc_number"`
	Format        string   `json:"source_format"`
	FSTRoot       string   `json:"fst_root,omitempty"`
	FSTTreeSHA256 string   `json:"fst_tree_sha256,omitempty"`
	Writable      bool     `json:"writable,omitempty"`
	SaveObjectID  string   `json:"save_object_id,omitempty"`
	CardOffset    int64    `json:"card_offset,omitempty"`
	CardSize      int64    `json:"card_size,omitempty"`
	GenerationID  string   `json:"generation_id,omitempty"`
}

type Extent struct {
	VirtualOffset int64    `json:"virtual_offset"`
	Length        int64    `json:"length"`
	SourcePath    string   `json:"source_path"`
	SourceOffset  int64    `json:"source_offset"`
	SourceSize    int64    `json:"source_size"`
	Identity      Identity `json:"source_fingerprint"`
	ReadOnly      bool     `json:"read_only"`
}

type MetadataExtent struct {
	VirtualOffset int64 `json:"virtual_offset"`
	Length        int64 `json:"length"`
	StorageOffset int64 `json:"storage_offset"`
}

type SaveExtent struct {
	VirtualOffset  int64  `json:"virtual_offset"`
	Length         int64  `json:"length"`
	SaveObjectID   string `json:"save_object_id"`
	CardOffset     int64  `json:"card_offset"`
	CardSize       int64  `json:"card_size"`
	GenerationID   string `json:"generation_id"`
	LayoutChecksum string `json:"layout_checksum"`
}

type Geometry struct {
	SectorSize        int64  `json:"sector_size"`
	PartitionStart    int64  `json:"partition_start_sector"`
	PartitionSectors  int64  `json:"partition_sectors"`
	ReservedSectors   int64  `json:"reserved_sectors"`
	NumberOfFATs      int64  `json:"number_of_fats"`
	FATSectors        int64  `json:"fat_sectors"`
	SectorsPerCluster int64  `json:"sectors_per_cluster"`
	ClusterSize       int64  `json:"cluster_size"`
	FirstDataSector   int64  `json:"first_data_sector"`
	VolumeID          uint32 `json:"volume_id"`
}

type Layout struct {
	Schema          int              `json:"schema"`
	VirtualSize     int64            `json:"virtual_size"`
	Geometry        Geometry         `json:"geometry"`
	MetadataExtents []MetadataExtent `json:"metadata_extents"`
	SourceExtents   []Extent         `json:"source_extents"`
	SaveExtents     []SaveExtent     `json:"save_extents,omitempty"`
	Files           []File           `json:"files"`
	MetadataHash    string           `json:"metadata_hash"`
	ExtentMapHash   string           `json:"extent_map_hash"`
	SaveExtentHash  string           `json:"save_extent_map_hash,omitempty"`
	LayoutChecksum  string           `json:"layout_checksum"`
}

type node struct {
	name      string
	full      string
	directory bool
	parent    *node
	children  []*node
	file      *File
	short     [11]byte
	first     uint32
	clusters  int64
}

func Build(virtualSize int64, label, identity string, files []File) (Layout, []byte, error) {
	if virtualSize <= (PartitionStart+reservedSectors)*SectorSize ||
		virtualSize%SectorSize != 0 || virtualSize/SectorSize > math.MaxUint32 {
		return Layout{}, nil, errors.New("invalid virtual FAT32 disk size")
	}
	if len(files) == 0 {
		return Layout{}, nil, errors.New("virtual FAT32 requires at least one file")
	}
	files = append([]File(nil), files...)
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].VirtualPath) < strings.ToLower(files[j].VirtualPath)
	})
	root, err := makeTree(files)
	if err != nil {
		return Layout{}, nil, err
	}
	assignShortNames(root)
	var directories, fileNodes []*node
	collectNodes(root, &directories, &fileNodes)
	for _, directory := range directories {
		entries := 1
		if directory != root {
			entries = 2
		}
		for _, child := range directory.children {
			entries += lfnSlots(child.name) + 1
		}
		directory.clusters = int64((entries*32 + int(ClusterSize) - 1) / int(ClusterSize))
		if directory.clusters < 1 {
			directory.clusters = 1
		}
	}
	var allocatedClusters int64
	for _, directory := range directories {
		allocatedClusters += directory.clusters
	}
	for _, file := range fileNodes {
		if file.file.LogicalSize < 0 || file.file.LogicalSize > math.MaxUint32 {
			return Layout{}, nil, fmt.Errorf("%s exceeds FAT32 file limit", file.full)
		}
		file.clusters = (file.file.LogicalSize + ClusterSize - 1) / ClusterSize
		if file.clusters == 0 {
			file.clusters = 1
		}
		allocatedClusters += file.clusters
	}
	totalClusters := allocatedClusters
	if totalClusters < minimumClusters {
		totalClusters = minimumClusters
	}
	fatSectors := ((totalClusters+2)*4 + SectorSize - 1) / SectorSize
	firstData := reservedSectors + numberOfFATs*fatSectors
	partitionSectors := virtualSize/SectorSize - PartitionStart
	availableClusters := (partitionSectors - firstData) / sectorsPerCluster
	if availableClusters < allocatedClusters || availableClusters > math.MaxUint32-2 {
		return Layout{}, nil, errors.New("virtual FAT32 capacity is insufficient")
	}
	totalClusters = availableClusters
	fatSectors = ((totalClusters+2)*4 + SectorSize - 1) / SectorSize
	firstData = reservedSectors + numberOfFATs*fatSectors
	availableClusters = (partitionSectors - firstData) / sectorsPerCluster
	if availableClusters < allocatedClusters {
		return Layout{}, nil, errors.New("virtual FAT32 geometry did not converge")
	}
	sum := sha256.Sum256([]byte(identity))
	volumeID := binary.LittleEndian.Uint32(sum[:4])
	if volumeID == 0 {
		volumeID = 0x57424743
	}
	next := uint32(2)
	fat := make([]byte, fatSectors*SectorSize)
	binary.LittleEndian.PutUint32(fat[0:4], 0x0ffffff8)
	binary.LittleEndian.PutUint32(fat[4:8], 0xffffffff)
	chain := func(count int64) uint32 {
		first := next
		for index := int64(0); index < count; index++ {
			value := uint32(0x0fffffff)
			if index+1 < count {
				value = next + 1
			}
			binary.LittleEndian.PutUint32(fat[int(next)*4:int(next)*4+4], value)
			next++
		}
		return first
	}
	for _, directory := range directories {
		directory.first = chain(directory.clusters)
	}
	for _, file := range fileNodes {
		file.first = chain(file.clusters)
	}
	metadataSize := 5*SectorSize + int64(len(fat))
	for _, directory := range directories {
		metadataSize += directory.clusters * ClusterSize
	}
	metadata := make([]byte, 0, metadataSize)
	metadataExtents := make([]MetadataExtent, 0, 5+int(numberOfFATs)+len(directories))
	addMetadata := func(virtualOffset int64, data []byte) {
		storageOffset := int64(len(metadata))
		metadata = append(metadata, data...)
		metadataExtents = append(metadataExtents, MetadataExtent{
			VirtualOffset: virtualOffset, Length: int64(len(data)), StorageOffset: storageOffset,
		})
	}
	addMetadata(0, makeMBR(PartitionStart, partitionSectors, volumeID))
	boot := makeBoot(partitionSectors, fatSectors, root.first, volumeID, label)
	addMetadata(PartitionStart*SectorSize, boot)
	addMetadata((PartitionStart+6)*SectorSize, boot)
	// Save writes stay inside already allocated card extents, so free-space
	// information remains exact throughout the generation's lifetime.
	freeClusters := uint32(availableClusters - int64(next-2))
	nextFree := next
	if freeClusters == 0 {
		nextFree = 0xffffffff
	}
	info := makeFSInfo(freeClusters, nextFree)
	addMetadata((PartitionStart+1)*SectorSize, info)
	addMetadata((PartitionStart+7)*SectorSize, info)
	fatStorageOffset := int64(len(metadata))
	for copyIndex := int64(0); copyIndex < numberOfFATs; copyIndex++ {
		virtualOffset := (PartitionStart + reservedSectors + copyIndex*fatSectors) * SectorSize
		if copyIndex == 0 {
			addMetadata(virtualOffset, fat)
		} else {
			// Both immutable virtual FATs expose the same bytes. Schema 2's
			// storage offsets already support sharing their stored copy.
			metadataExtents = append(metadataExtents, MetadataExtent{
				VirtualOffset: virtualOffset, Length: int64(len(fat)), StorageOffset: fatStorageOffset,
			})
		}
	}
	dataStart := (PartitionStart + firstData) * SectorSize
	for _, directory := range directories {
		data, buildErr := buildDirectory(directory, root, label)
		if buildErr != nil {
			return Layout{}, nil, buildErr
		}
		virtualOffset := dataStart + int64(directory.first-2)*ClusterSize
		addMetadata(virtualOffset, data)
	}
	var sourceExtents []Extent
	var saveExtents []SaveExtent
	for _, file := range fileNodes {
		virtualOffset := dataStart + int64(file.first-2)*ClusterSize
		file.file.VirtualOffset = virtualOffset
		file.file.AllocatedSize = file.clusters * ClusterSize
		if file.file.Writable {
			if file.file.SaveObjectID == "" || file.file.CardSize <= 0 ||
				file.file.LogicalSize != file.file.CardSize ||
				file.file.CardOffset < 0 ||
				file.file.CardOffset > file.file.CardSize-file.file.LogicalSize ||
				file.file.SourcePath != "" {
				return Layout{}, nil, errors.New("invalid writable save file")
			}
			saveExtents = append(saveExtents, SaveExtent{
				VirtualOffset: virtualOffset, Length: file.file.LogicalSize,
				SaveObjectID: file.file.SaveObjectID, CardOffset: file.file.CardOffset,
				CardSize: file.file.CardSize, GenerationID: file.file.GenerationID,
			})
			continue
		}
		if file.file.SourcePath == "" {
			return Layout{}, nil, errors.New("immutable virtual file has no source")
		}
		sourceExtents = append(sourceExtents, Extent{
			VirtualOffset: virtualOffset, Length: file.file.LogicalSize,
			SourcePath: file.file.SourcePath, SourceOffset: file.file.SourceOffset,
			SourceSize: file.file.SourceSize, Identity: file.file.Identity, ReadOnly: true,
		})
	}
	sort.Slice(metadataExtents, func(i, j int) bool {
		return metadataExtents[i].VirtualOffset < metadataExtents[j].VirtualOffset
	})
	sort.Slice(sourceExtents, func(i, j int) bool {
		return sourceExtents[i].VirtualOffset < sourceExtents[j].VirtualOffset
	})
	sort.Slice(saveExtents, func(i, j int) bool {
		return saveExtents[i].VirtualOffset < saveExtents[j].VirtualOffset
	})
	if err = validateRanges(virtualSize, metadataExtents, sourceExtents, saveExtents); err != nil {
		return Layout{}, nil, err
	}
	metadataSum := sha256.Sum256(metadata)
	metadataHash := hex.EncodeToString(metadataSum[:])
	extentHash := hashExtents(sourceExtents)
	saveBaseHash := hashSaveExtents(saveExtents, false)
	layoutSum := sha256.Sum256([]byte(metadataHash + "\x00" + extentHash + "\x00" + saveBaseHash))
	layoutChecksum := hex.EncodeToString(layoutSum[:])
	for index := range saveExtents {
		saveExtents[index].LayoutChecksum = layoutChecksum
	}
	return Layout{
		Schema: 2, VirtualSize: virtualSize,
		Geometry: Geometry{
			SectorSize: SectorSize, PartitionStart: PartitionStart,
			PartitionSectors: partitionSectors, ReservedSectors: reservedSectors,
			NumberOfFATs: numberOfFATs, FATSectors: fatSectors,
			SectorsPerCluster: sectorsPerCluster, ClusterSize: ClusterSize,
			FirstDataSector: firstData, VolumeID: volumeID,
		},
		MetadataExtents: metadataExtents, SourceExtents: sourceExtents,
		SaveExtents: saveExtents, Files: files,
		MetadataHash: metadataHash, ExtentMapHash: extentHash,
		SaveExtentHash: hashSaveExtents(saveExtents, true), LayoutChecksum: layoutChecksum,
	}, metadata, nil
}

func hashExtents(extents []Extent) string {
	hash := sha256.New()
	for _, extent := range extents {
		fmt.Fprintf(hash, "%d\x00%d\x00%s\x00%d\x00%d\x00%s\n",
			extent.VirtualOffset, extent.Length, extent.SourcePath, extent.SourceOffset,
			extent.SourceSize, extent.Identity.SHA256)
		if extent.Identity.FilesystemID != "" {
			fmt.Fprintf(hash, "filesystem-id\x00%s\n", extent.Identity.FilesystemID)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hashSaveExtents(extents []SaveExtent, includeLayout bool) string {
	hash := sha256.New()
	for _, extent := range extents {
		layout := ""
		if includeLayout {
			layout = extent.LayoutChecksum
		}
		fmt.Fprintf(hash, "%d\x00%d\x00%s\x00%d\x00%d\x00%s\x00%s\n",
			extent.VirtualOffset, extent.Length, extent.SaveObjectID, extent.CardOffset,
			extent.CardSize, extent.GenerationID, layout)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func makeTree(files []File) (*node, error) {
	root := &node{directory: true, full: "/"}
	directories := map[string]*node{"/": root}
	seen := make(map[string]struct{}, len(files))
	for index := range files {
		file := &files[index]
		clean := path.Clean(file.VirtualPath)
		if clean == "." || clean == "/" || !strings.HasPrefix(clean, "/") ||
			strings.Contains(clean, `\`) {
			return nil, fmt.Errorf("unsafe virtual path %q", file.VirtualPath)
		}
		key := strings.ToLower(clean)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate case-insensitive FAT path %q", clean)
		}
		seen[key] = struct{}{}
		parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
		parent := root
		current := ""
		for partIndex, part := range parts {
			if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) {
				return nil, fmt.Errorf("unsafe virtual path component %q", part)
			}
			current += "/" + part
			if partIndex+1 < len(parts) {
				dirKey := strings.ToLower(current)
				directory := directories[dirKey]
				if directory == nil {
					directory = &node{name: part, full: current, directory: true, parent: parent}
					parent.children = append(parent.children, directory)
					directories[dirKey] = directory
				}
				parent = directory
				continue
			}
			child := &node{name: part, full: current, parent: parent, file: file}
			parent.children = append(parent.children, child)
		}
	}
	var sortTree func(*node)
	sortTree = func(directory *node) {
		sort.Slice(directory.children, func(i, j int) bool {
			return strings.ToLower(directory.children[i].name) <
				strings.ToLower(directory.children[j].name)
		})
		for _, child := range directory.children {
			if child.directory {
				sortTree(child)
			}
		}
	}
	sortTree(root)
	return root, nil
}

func collectNodes(current *node, directories, files *[]*node) {
	if current.directory {
		*directories = append(*directories, current)
		for _, child := range current.children {
			collectNodes(child, directories, files)
		}
		return
	}
	*files = append(*files, current)
}

func assignShortNames(directory *node) {
	used := make(map[string]struct{})
	for _, child := range directory.children {
		extension := ""
		base := child.name
		if !child.directory {
			extension = strings.TrimPrefix(path.Ext(child.name), ".")
			base = strings.TrimSuffix(child.name, path.Ext(child.name))
		}
		base = shortChars(base)
		extension = shortChars(extension)
		candidate := ""
		if len(base) > 0 && len(base) <= 8 && len(extension) <= 3 &&
			isPlain83(child.name, child.directory) {
			candidate = fmt.Sprintf("%-8s%-3s", base, extension)
		}
		if candidate == "" {
			prefix := base
			if prefix == "" {
				prefix = "FILE"
			}
			if len(prefix) > 6 {
				prefix = prefix[:6]
			}
			for sequence := 1; ; sequence++ {
				suffix := fmt.Sprintf("~%d", sequence)
				trim := 8 - len(suffix)
				value := prefix
				if len(value) > trim {
					value = value[:trim]
				}
				candidate = fmt.Sprintf("%-8s%-3s", value+suffix, truncate(extension, 3))
				if _, exists := used[candidate]; !exists {
					break
				}
			}
		}
		used[candidate] = struct{}{}
		copy(child.short[:], candidate)
		if child.directory {
			assignShortNames(child)
		}
	}
}

func shortChars(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToUpper(value) {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') ||
			strings.ContainsRune("$%'-_@~`!(){}^#&", character) {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func isPlain83(name string, directory bool) bool {
	if name != strings.ToUpper(name) || strings.ContainsAny(name, " +,;=[]") {
		return false
	}
	if directory {
		return len(name) > 0 && len(name) <= 8 && !strings.Contains(name, ".")
	}
	extension := strings.TrimPrefix(path.Ext(name), ".")
	base := strings.TrimSuffix(name, path.Ext(name))
	return len(base) > 0 && len(base) <= 8 && len(extension) <= 3
}

func truncate(value string, length int) string {
	if len(value) > length {
		return value[:length]
	}
	return value
}

func lfnSlots(name string) int {
	return (len(utf16.Encode([]rune(name))) + 12) / 13
}

func buildDirectory(directory, root *node, label string) ([]byte, error) {
	data := make([]byte, directory.clusters*ClusterSize)
	offset := 0
	if directory == root {
		var volume [11]byte
		copy(volume[:], fmt.Sprintf("%-11s", truncate(shortChars(label), 11)))
		writeEntry(data[offset:offset+32], volume, 0, 0, 0x08)
		offset += 32
	} else {
		var dot, dotdot [11]byte
		copy(dot[:], ".          ")
		copy(dotdot[:], "..         ")
		writeEntry(data[offset:offset+32], dot, directory.first, 0, 0x10)
		offset += 32
		parentCluster := uint32(0)
		if directory.parent != root {
			parentCluster = directory.parent.first
		}
		writeEntry(data[offset:offset+32], dotdot, parentCluster, 0, 0x10)
		offset += 32
	}
	for _, child := range directory.children {
		entries := makeLFN(child.name, lfnChecksum(child.short[:]))
		for _, entry := range entries {
			if offset+32 > len(data) {
				return nil, errors.New("FAT32 directory allocation overflow")
			}
			copy(data[offset:offset+32], entry)
			offset += 32
		}
		attribute := byte(0x20)
		size := uint32(0)
		if child.directory {
			attribute = 0x10
		} else {
			size = uint32(child.file.LogicalSize)
		}
		writeEntry(data[offset:offset+32], child.short, child.first, size, attribute)
		offset += 32
	}
	return data, nil
}

func makeLFN(name string, checksum byte) [][]byte {
	code := utf16.Encode([]rune(name))
	slots := (len(code) + 12) / 13
	entries := make([][]byte, 0, slots)
	positions := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
	for diskIndex := slots; diskIndex >= 1; diskIndex-- {
		entry := make([]byte, 32)
		for index := range entry {
			entry[index] = 0xff
		}
		sequence := byte(diskIndex)
		if diskIndex == slots {
			sequence |= 0x40
		}
		entry[0], entry[11], entry[12], entry[13] = sequence, 0x0f, 0, checksum
		entry[26], entry[27] = 0, 0
		start := (diskIndex - 1) * 13
		for index, position := range positions {
			codeIndex := start + index
			value := uint16(0xffff)
			if codeIndex < len(code) {
				value = code[codeIndex]
			} else if codeIndex == len(code) {
				value = 0
			}
			binary.LittleEndian.PutUint16(entry[position:position+2], value)
		}
		entries = append(entries, entry)
	}
	return entries
}

func writeEntry(target []byte, name [11]byte, cluster uint32, size uint32, attribute byte) {
	clear(target)
	copy(target[:11], name[:])
	target[11] = attribute
	binary.LittleEndian.PutUint16(target[20:22], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(target[26:28], uint16(cluster))
	binary.LittleEndian.PutUint32(target[28:32], size)
}

func lfnChecksum(short []byte) byte {
	var sum byte
	for _, value := range short[:11] {
		sum = ((sum & 1) << 7) + (sum >> 1) + value
	}
	return sum
}

func makeMBR(start, sectors int64, signature uint32) []byte {
	data := make([]byte, SectorSize)
	binary.LittleEndian.PutUint32(data[440:444], signature)
	partition := data[446:462]
	partition[0], partition[4] = 0, 0x0c
	partition[1], partition[2], partition[3] = 0x20, 0x21, 0
	partition[5], partition[6], partition[7] = 0xfe, 0xff, 0xff
	binary.LittleEndian.PutUint32(partition[8:12], uint32(start))
	binary.LittleEndian.PutUint32(partition[12:16], uint32(sectors))
	data[510], data[511] = 0x55, 0xaa
	return data
}

func makeBoot(sectors, fatSectors int64, root, volumeID uint32, label string) []byte {
	data := make([]byte, SectorSize)
	copy(data[0:3], []byte{0xeb, 0x58, 0x90})
	copy(data[3:11], "WIIBRDG ")
	binary.LittleEndian.PutUint16(data[11:13], uint16(SectorSize))
	data[13] = byte(sectorsPerCluster)
	binary.LittleEndian.PutUint16(data[14:16], uint16(reservedSectors))
	data[16], data[21] = byte(numberOfFATs), 0xf8
	binary.LittleEndian.PutUint16(data[24:26], 63)
	binary.LittleEndian.PutUint16(data[26:28], 255)
	binary.LittleEndian.PutUint32(data[28:32], uint32(PartitionStart))
	binary.LittleEndian.PutUint32(data[32:36], uint32(sectors))
	binary.LittleEndian.PutUint32(data[36:40], uint32(fatSectors))
	binary.LittleEndian.PutUint32(data[44:48], root)
	binary.LittleEndian.PutUint16(data[48:50], 1)
	binary.LittleEndian.PutUint16(data[50:52], 6)
	data[64], data[66] = 0x80, 0x29
	binary.LittleEndian.PutUint32(data[67:71], volumeID)
	copy(data[71:82], fmt.Sprintf("%-11s", truncate(shortChars(label), 11)))
	copy(data[82:90], "FAT32   ")
	data[510], data[511] = 0x55, 0xaa
	return data
}

func makeFSInfo(freeClusters, nextFree uint32) []byte {
	data := make([]byte, SectorSize)
	copy(data[0:4], "RRaA")
	copy(data[484:488], "rrAa")
	binary.LittleEndian.PutUint32(data[488:492], freeClusters)
	binary.LittleEndian.PutUint32(data[492:496], nextFree)
	data[510], data[511] = 0x55, 0xaa
	return data
}

func validateRanges(size int64, metadata []MetadataExtent, sources []Extent,
	saves []SaveExtent,
) error {
	type span struct{ start, end int64 }
	var spans []span
	for _, item := range metadata {
		if item.VirtualOffset < 0 || item.Length <= 0 || item.VirtualOffset > size-item.Length {
			return errors.New("metadata extent exceeds virtual disk")
		}
		spans = append(spans, span{item.VirtualOffset, item.VirtualOffset + item.Length})
	}
	for _, item := range sources {
		if item.VirtualOffset < 0 || item.Length < 0 || item.SourceOffset < 0 ||
			item.SourceOffset > item.SourceSize-item.Length ||
			item.VirtualOffset > size-item.Length {
			return errors.New("source extent exceeds source or virtual disk")
		}
		spans = append(spans, span{item.VirtualOffset, item.VirtualOffset + item.Length})
	}
	saveObjects := make(map[string]struct{}, len(saves))
	for _, item := range saves {
		if item.VirtualOffset < 0 || item.Length <= 0 || item.SaveObjectID == "" ||
			item.CardOffset < 0 || item.CardSize <= 0 ||
			item.CardOffset > item.CardSize-item.Length ||
			item.VirtualOffset > size-item.Length {
			return errors.New("save extent exceeds card or virtual disk")
		}
		if _, duplicate := saveObjects[item.SaveObjectID]; duplicate {
			return errors.New("duplicate writable save object extent")
		}
		saveObjects[item.SaveObjectID] = struct{}{}
		spans = append(spans, span{item.VirtualOffset, item.VirtualOffset + item.Length})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for index := 1; index < len(spans); index++ {
		if spans[index].start < spans[index-1].end {
			return errors.New("virtual FAT32 extents overlap")
		}
	}
	return nil
}
