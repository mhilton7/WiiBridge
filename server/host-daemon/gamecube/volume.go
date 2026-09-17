package gamecube

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	diskfs "github.com/diskfs/go-diskfs"
	diskpkg "github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/mbr"
)

const (
	VolumeSchema = 3
	// Keep the exported disk below the 32 GiB boundary used by older 32-bit
	// NBD clients while retaining ample room for two GameCube discs and saves.
	VolumeSize        = int64(8 << 30)
	VolumeClusterSize = 4 << 10
	VolumeSectorSize  = int64(512)
	VolumeStartSector = uint32(2048)
)

var unsafeFATName = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

type MemoryCardMode string

const (
	MemoryCardPhysical           MemoryCardMode = "physical"
	MemoryCardEmulated           MemoryCardMode = "emulated" // Legacy copied per-title volume only.
	MemoryCardEmulatedIndividual MemoryCardMode = "emulated-individual"
	MemoryCardEmulatedShared     MemoryCardMode = "emulated-shared"
)

func (mode MemoryCardMode) IsLibraryEmulated() bool {
	return mode == MemoryCardEmulatedIndividual || mode == MemoryCardEmulatedShared
}

type VolumeManifest struct {
	Schema       int            `json:"schema"`
	CacheKey     string         `json:"cache_key"`
	ImagePath    string         `json:"image_path"`
	Game         Game           `json:"game"`
	Mode         MemoryCardMode `json:"memory_card_mode"`
	VolumeSize   int64          `json:"volume_size"`
	ClusterSize  int            `json:"cluster_size"`
	RuntimePaths []string       `json:"runtime_paths"`
	Created      time.Time      `json:"created_utc"`
	Complete     bool           `json:"complete"`
}

func CacheKey(game Game, mode MemoryCardMode) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "gamecube-volume-v%d\x00%s\x00%d\x00%s", VolumeSchema, game.ID, game.Revision, mode)
	for _, disc := range game.Discs {
		fmt.Fprintf(hash, "\x00%d\x00%s\x00%s", disc.Number, disc.Format, disc.SHA256)
	}
	return fmt.Sprintf("gc-v%d-%s", VolumeSchema, hex.EncodeToString(hash.Sum(nil)[:16]))
}

func BuildVolume(ctx context.Context, cacheRoot string, game Game, mode MemoryCardMode) (VolumeManifest, error) {
	if game.Validation != "valid" || len(game.Discs) == 0 || len(game.Discs) > 2 {
		return VolumeManifest{}, errors.New("only completely validated one- or two-disc games can be exported")
	}
	if mode != MemoryCardPhysical && mode != MemoryCardEmulated {
		return VolumeManifest{}, errors.New("invalid memory-card mode")
	}
	var err error
	game, err = hashGameSourcesContext(ctx, game)
	if err != nil {
		return VolumeManifest{}, err
	}
	key := CacheKey(game, mode)
	ready := filepath.Join(cacheRoot, "ready", key)
	manifestPath := filepath.Join(ready, "manifest.json")
	if manifest, err := LoadAndValidateVolume(manifestPath); err == nil {
		return manifest, nil
	}
	buildingRoot := filepath.Join(cacheRoot, ".building")
	if err := os.MkdirAll(buildingRoot, 0o700); err != nil {
		return VolumeManifest{}, err
	}
	if err := os.MkdirAll(filepath.Join(cacheRoot, "ready"), 0o700); err != nil {
		return VolumeManifest{}, err
	}
	building, err := os.MkdirTemp(buildingRoot, key+"-")
	if err != nil {
		return VolumeManifest{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(building)
		}
	}()
	imagePath := filepath.Join(building, "gamecube.img")
	if err = createVolumeImage(ctx, imagePath, game); err != nil {
		return VolumeManifest{}, err
	}
	manifest := VolumeManifest{
		Schema: VolumeSchema, CacheKey: key, ImagePath: filepath.Join(ready, "gamecube.img"),
		Game: game, Mode: mode, VolumeSize: VolumeSize, ClusterSize: VolumeClusterSize,
		Created: time.Now().UTC(), Complete: true,
	}
	titleDir := volumeTitle(game)
	if game.Discs[0].Format == "fst" {
		manifest.RuntimePaths = []string{"/games/" + titleDir + "/sys/boot.bin"}
	} else {
		manifest.RuntimePaths = []string{"/games/" + titleDir + "/" + runtimeName(game.Discs[0], false)}
		if len(game.Discs) == 2 {
			manifest.RuntimePaths = append(manifest.RuntimePaths,
				"/games/"+titleDir+"/"+runtimeName(game.Discs[1], true))
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return VolumeManifest{}, err
	}
	if err = os.WriteFile(filepath.Join(building, "manifest.json"), data, 0o600); err != nil {
		return VolumeManifest{}, err
	}
	if _, err = ValidateVolume(imagePath, manifest); err != nil {
		return VolumeManifest{}, err
	}
	if err = os.Rename(building, ready); err != nil {
		return VolumeManifest{}, err
	}
	committed = true
	return manifest, nil
}

func hashGameSources(game Game) (Game, error) {
	return hashGameSourcesContext(context.Background(), game)
}

func hashGameSourcesContext(ctx context.Context, game Game) (Game, error) {
	game.Discs = append([]Disc(nil), game.Discs...)
	for index := range game.Discs {
		if err := ctx.Err(); err != nil {
			return Game{}, err
		}
		if game.Discs[index].SHA256 != "" {
			continue
		}
		var (
			sum string
			err error
		)
		if game.Discs[index].Format == "fst" {
			sum, _, err = hashTreeContext(ctx, game.Discs[index].SourcePath)
		} else {
			sum, err = hashFileContext(ctx, game.Discs[index].SourcePath)
		}
		if err != nil {
			return Game{}, fmt.Errorf("hash disc %d: %w", game.Discs[index].Number+1, err)
		}
		game.Discs[index].SHA256 = sum
	}
	return game, nil
}

func createVolumeImage(ctx context.Context, path string, game Game) error {
	disk, err := diskfs.Create(path, VolumeSize, diskfs.SectorSizeDefault)
	if err != nil {
		return err
	}
	defer disk.Close()
	sectors := uint32(VolumeSize / VolumeSectorSize)
	table := &mbr.Table{
		LogicalSectorSize: int(VolumeSectorSize), PhysicalSectorSize: int(VolumeSectorSize),
		Partitions: []*mbr.Partition{{
			Index: 1, Type: mbr.Fat32LBA, Start: VolumeStartSector,
			Size: sectors - VolumeStartSector,
		}},
	}
	if err = disk.Partition(table); err != nil {
		return err
	}
	fat, err := disk.CreateFilesystem(diskpkg.FilesystemSpec{
		Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "WIIBRIDGE",
		Reproducible: true,
	})
	if err != nil {
		return err
	}
	defer fat.Close()
	for _, directory := range []string{"/games", "/saves", "/controllers", "/apps", "/apps/Nintendont"} {
		if err = fat.Mkdir(directory); err != nil {
			return err
		}
	}
	titlePath := "/games/" + volumeTitle(game)
	if err = fat.Mkdir(titlePath); err != nil {
		return err
	}
	for index, disc := range game.Discs {
		if err = ctx.Err(); err != nil {
			return err
		}
		if disc.Format == "fst" {
			if index != 0 || len(game.Discs) != 1 {
				return errors.New("two-disc extracted FST sets are unsupported by Nintendont")
			}
			if err = copyTreeToFilesystem(ctx, fat, disc.SourcePath, titlePath); err != nil {
				return err
			}
			continue
		}
		name := runtimeName(disc, index == 1)
		if err = copyFileToFilesystem(ctx, fat, disc.SourcePath, titlePath+"/"+name); err != nil {
			return err
		}
	}
	return patchGeometry(path)
}

func runtimeName(disc Disc, second bool) string {
	base := "game"
	if second {
		base = "disc2"
	}
	if disc.Format == "ciso" {
		return base + ".ciso"
	}
	return base + ".iso"
}

func volumeTitle(game Game) string {
	title := strings.TrimSpace(unsafeFATName.ReplaceAllString(game.Title, "_"))
	title = strings.TrimRight(title, ". ")
	if title == "" {
		title = game.ID
	}
	const maxTitleBytes = 180
	if len(title) > maxTitleBytes {
		title = title[:maxTitleBytes]
	}
	return fmt.Sprintf("%s [%s]", title, game.ID)
}

func copyFileToFilesystem(ctx context.Context, target filesystem.FileSystem, sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	targetFile, err := target.OpenFile(targetPath, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	defer targetFile.Close()
	buffer := make([]byte, 1<<20)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			written, writeErr := targetFile.Write(buffer[:n])
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func copyTreeToFilesystem(ctx context.Context, target filesystem.FileSystem, sourceRoot, targetRoot string) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink forbidden in extracted FST")
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		targetPath := targetRoot + "/" + filepath.ToSlash(relative)
		if entry.IsDir() {
			return target.Mkdir(targetPath)
		}
		if !entry.Type().IsRegular() {
			return errors.New("special file forbidden in extracted FST")
		}
		return copyFileToFilesystem(ctx, target, path, targetPath)
	})
}

func patchGeometry(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	bootOffset := int64(VolumeStartSector) * VolumeSectorSize
	boot := make([]byte, 512)
	if _, err = file.ReadAt(boot, bootOffset); err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(boot[24:26], 63)
	binary.LittleEndian.PutUint16(boot[26:28], 255)
	binary.LittleEndian.PutUint32(boot[28:32], VolumeStartSector)
	if _, err = file.WriteAt(boot, bootOffset); err != nil {
		return err
	}
	backupSector := binary.LittleEndian.Uint16(boot[50:52])
	if backupSector == 0 {
		return errors.New("FAT32 backup boot sector is missing")
	}
	if _, err = file.WriteAt(boot, bootOffset+int64(backupSector)*VolumeSectorSize); err != nil {
		return err
	}
	return file.Sync()
}

type VolumeValidation struct {
	MBR              bool     `json:"mbr_valid"`
	FAT32            bool     `json:"fat32_valid"`
	BackupBoot       bool     `json:"backup_boot_matches"`
	ClusterSize      int      `json:"cluster_size"`
	Capacity         int64    `json:"capacity"`
	RequiredPaths    []string `json:"required_paths"`
	RequiredPathsOK  bool     `json:"required_paths_valid"`
	NintendontLayout bool     `json:"nintendont_layout_valid"`
}

func ValidateVolume(path string, manifest VolumeManifest) (VolumeValidation, error) {
	file, err := os.Open(path)
	if err != nil {
		return VolumeValidation{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return VolumeValidation{}, err
	}
	if info.Size() != VolumeSize {
		return VolumeValidation{}, fmt.Errorf("volume capacity %d, want %d", info.Size(), VolumeSize)
	}
	mbrSector := make([]byte, 512)
	if _, err = file.ReadAt(mbrSector, 0); err != nil {
		return VolumeValidation{}, err
	}
	start := binary.LittleEndian.Uint32(mbrSector[454:458])
	if mbrSector[510] != 0x55 || mbrSector[511] != 0xaa ||
		mbrSector[450] != byte(mbr.Fat32LBA) || start != VolumeStartSector {
		return VolumeValidation{}, errors.New("invalid MBR or FAT32 partition")
	}
	boot := make([]byte, 512)
	bootOffset := int64(start) * VolumeSectorSize
	if _, err = file.ReadAt(boot, bootOffset); err != nil {
		return VolumeValidation{}, err
	}
	bytesPerSector := int(binary.LittleEndian.Uint16(boot[11:13]))
	sectorsPerCluster := int(boot[13])
	clusterSize := bytesPerSector * sectorsPerCluster
	if bytesPerSector != 512 || clusterSize != VolumeClusterSize ||
		string(boot[82:90]) != "FAT32   " || binary.LittleEndian.Uint32(boot[28:32]) != start {
		return VolumeValidation{}, errors.New("invalid FAT32 geometry")
	}
	backup := make([]byte, 512)
	backupSector := binary.LittleEndian.Uint16(boot[50:52])
	if backupSector == 0 {
		return VolumeValidation{}, errors.New("missing backup boot sector")
	}
	if _, err = file.ReadAt(backup, bootOffset+int64(backupSector)*VolumeSectorSize); err != nil {
		return VolumeValidation{}, err
	}
	if !bytesEqual(boot, backup) {
		return VolumeValidation{}, errors.New("backup boot sector differs")
	}
	disk, err := diskfs.Open(path, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		return VolumeValidation{}, err
	}
	defer disk.Close()
	fat, err := disk.GetFilesystem(1)
	if err != nil {
		return VolumeValidation{}, err
	}
	defer fat.Close()
	required := append([]string{"/games", "/saves", "/controllers", "/apps/Nintendont"}, manifest.RuntimePaths...)
	for _, requiredPath := range required {
		if _, err = fat.Stat(strings.TrimPrefix(requiredPath, "/")); err != nil {
			return VolumeValidation{}, fmt.Errorf("required path %s: %w", requiredPath, err)
		}
	}
	return VolumeValidation{
		MBR: true, FAT32: true, BackupBoot: true, ClusterSize: clusterSize,
		Capacity: info.Size(), RequiredPaths: required, RequiredPathsOK: true,
		NintendontLayout: true,
	}, nil
}

func LoadAndValidateVolume(manifestPath string) (VolumeManifest, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return VolumeManifest{}, err
	}
	var manifest VolumeManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return VolumeManifest{}, err
	}
	if manifest.Schema != VolumeSchema || !manifest.Complete ||
		manifest.CacheKey != CacheKey(manifest.Game, manifest.Mode) {
		return VolumeManifest{}, errors.New("stale or incomplete GameCube cache manifest")
	}
	if _, err = ValidateVolume(manifest.ImagePath, manifest); err != nil {
		return VolumeManifest{}, err
	}
	return manifest, nil
}

// FindReadyVolume resolves a prepared export from stable catalog metadata.
// Catalog scans intentionally defer full hashes, while ready manifests contain
// those hashes, so their cache keys cannot be recomputed from scan results.
func FindReadyVolume(cacheRoot string, game Game, mode MemoryCardMode) (VolumeManifest, string, error) {
	readyRoot := filepath.Join(cacheRoot, "ready")
	entries, err := os.ReadDir(readyRoot)
	if err != nil {
		return VolumeManifest{}, "", err
	}
	var (
		selected     VolumeManifest
		selectedPath string
	)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(readyRoot, entry.Name(), "manifest.json")
		manifest, loadErr := LoadAndValidateVolume(manifestPath)
		if loadErr != nil || !sameCatalogSource(manifest, game, mode) {
			continue
		}
		if selectedPath == "" || manifest.Created.After(selected.Created) {
			selected, selectedPath = manifest, manifestPath
		}
	}
	if selectedPath == "" {
		return VolumeManifest{}, "", os.ErrNotExist
	}
	return selected, selectedPath, nil
}

func sameCatalogSource(manifest VolumeManifest, game Game, mode MemoryCardMode) bool {
	if manifest.Mode != mode || manifest.Game.ID != game.ID ||
		manifest.Game.Revision != game.Revision ||
		len(manifest.Game.Discs) != len(game.Discs) {
		return false
	}
	for index := range game.Discs {
		cached, current := manifest.Game.Discs[index], game.Discs[index]
		if cached.Number != current.Number || cached.Format != current.Format ||
			cached.SourcePath != current.SourcePath ||
			cached.PhysicalSize != current.PhysicalSize {
			return false
		}
	}
	return true
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

type FileBackend struct {
	file     *os.File
	size     int64
	writable bool
}

func OpenFileBackend(path string, writable bool) (*FileBackend, error) {
	flag := os.O_RDONLY
	if writable {
		flag = os.O_RDWR
	}
	file, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &FileBackend{file: file, size: info.Size(), writable: writable}, nil
}

func (b *FileBackend) Size() int64                             { return b.size }
func (b *FileBackend) ReadOnly() bool                          { return !b.writable }
func (b *FileBackend) ReadAt(p []byte, off int64) (int, error) { return b.file.ReadAt(p, off) }
func (b *FileBackend) WriteAt(p []byte, off int64) (int, error) {
	if !b.writable {
		return 0, os.ErrPermission
	}
	return b.file.WriteAt(p, off)
}
func (b *FileBackend) Sync() error  { return b.file.Sync() }
func (b *FileBackend) Close() error { return b.file.Close() }
