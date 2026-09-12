package gamecube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"wiibridge/server/host-daemon/fat32virtual"
	"wiibridge/shared/perf"
)

const (
	LibrarySchema           = 2
	validationReceiptSchema = 1
	libraryFileCacheLimit   = 32
	libraryMinimumSize      = int64(8 << 30)
	libraryAlignment        = int64(1 << 30)
	libraryMetadataReserve  = int64(256 << 20)
	fat32MaximumFileSize    = int64(1<<32 - 1)
)

var (
	ErrGameCubeSourceUnavailable = errors.New("GameCube source unavailable")
	ErrGameCubeSourceChanged     = errors.New("GameCube source identity changed")
)

type LibraryConfig struct {
	HeadroomPercent int
	SaveReserveMiB  int64
	MaxVolumeGiB    int64
	Mode            MemoryCardMode
	Retention       int
	SourceRoot      string
	SavesRoot       string
	CardSize        int64
	AutoCreateCards bool
	SharedCardName  string
	MaxSaveBackups  int
	Application     string
}

func DefaultLibraryConfig() LibraryConfig {
	return LibraryConfig{
		HeadroomPercent: 5, SaveReserveMiB: 1024,
		Mode: MemoryCardPhysical, Retention: 2,
		CardSize: DefaultLibraryCardSize, AutoCreateCards: true,
		MaxSaveBackups: DefaultSaveBackupRetention,
	}
}

func (config LibraryConfig) Validate() error {
	if config.HeadroomPercent < 0 || config.HeadroomPercent > 100 {
		return errors.New("GameCube headroom percent must be between 0 and 100")
	}
	if config.SaveReserveMiB < 0 || config.SaveReserveMiB > math.MaxInt64>>20 {
		return errors.New("invalid GameCube save reserve")
	}
	if config.MaxVolumeGiB < 0 || config.MaxVolumeGiB > math.MaxInt64>>30 {
		return errors.New("invalid GameCube maximum volume")
	}
	if config.Mode == MemoryCardEmulated {
		return errors.New("legacy copied-volume emulated mode is unavailable; select emulated-individual or emulated-shared")
	}
	if config.Mode != MemoryCardPhysical && !config.Mode.IsLibraryEmulated() {
		return errors.New("invalid GameCube library memory-card mode")
	}
	if config.Mode.IsLibraryEmulated() {
		if config.SavesRoot == "" {
			return errors.New("GameCube emulated mode requires managed save storage")
		}
		if !SupportedSaveCardSize(config.CardSize) {
			return errors.New("GameCube memory-card size is unsupported")
		}
		if config.Mode == MemoryCardEmulatedShared &&
			!sharedCardPattern.MatchString(config.SharedCardName) {
			return errors.New("GameCube shared memory-card selection is invalid")
		}
	}
	if config.MaxSaveBackups < 1 || config.MaxSaveBackups > 100 {
		return errors.New("GameCube save backup retention must be between 1 and 100")
	}
	if config.Retention < 1 || config.Retention > 20 {
		return errors.New("GameCube generation retention must be between 1 and 20")
	}
	return nil
}

type LibraryTitle struct {
	Title      string `json:"title"`
	ID         string `json:"game_id"`
	Revision   byte   `json:"revision"`
	Region     string `json:"region"`
	Format     string `json:"source_format"`
	DiscCount  int    `json:"disc_count"`
	OutputDir  string `json:"output_directory"`
	PreparedID string `json:"prepared_artifact_identity"`
}

type LibraryManifest struct {
	Schema             int                   `json:"schema"`
	GenerationID       string                `json:"generation_id"`
	Created            time.Time             `json:"created_utc"`
	VolumeSize         int64                 `json:"virtual_disk_size"`
	Filesystem         string                `json:"filesystem"`
	Geometry           fat32virtual.Geometry `json:"fat32_geometry"`
	ClusterSize        int64                 `json:"cluster_size"`
	Mode               MemoryCardMode        `json:"memory_card_mode"`
	ReadOnly           bool                  `json:"read_only"`
	TitleCount         int                   `json:"total_title_count"`
	DiscCount          int                   `json:"total_disc_count"`
	MappedFileCount    int                   `json:"mapped_file_count"`
	MappedExtentCount  int                   `json:"mapped_extent_count"`
	CatalogFingerprint string                `json:"source_catalog_fingerprint"`
	MetadataHash       string                `json:"metadata_hash"`
	ExtentMapHash      string                `json:"extent_map_hash"`
	LayoutPath         string                `json:"layout_path"`
	MetadataPath       string                `json:"metadata_path"`
	LibraryRoot        string                `json:"library_root"`
	Titles             []LibraryTitle        `json:"titles"`
	Files              []fat32virtual.File   `json:"virtual_files"`
	Complete           bool                  `json:"complete"`
	LegacyImagePath    string                `json:"image_path,omitempty"`
	SaveOverlayVersion int                   `json:"save_overlay_format_version,omitempty"`
	SaveObjects        []SaveObject          `json:"save_objects,omitempty"`
	SaveExtentCount    int                   `json:"writable_save_extent_count,omitempty"`
	SaveExtentHash     string                `json:"writable_save_extent_hash,omitempty"`
	LayoutChecksum     string                `json:"layout_checksum,omitempty"`
	MaxSaveBackups     int                   `json:"maximum_save_backups,omitempty"`
	Application        string                `json:"application_version,omitempty"`
}

type LibraryBuildProgress struct {
	State              string    `json:"state"`
	GenerationID       string    `json:"generation_id,omitempty"`
	GamesCompleted     int       `json:"titles_processed"`
	TotalGames         int       `json:"total_titles"`
	DiscsCompleted     int       `json:"discs_processed"`
	TotalDiscs         int       `json:"total_discs"`
	FilesMapped        int       `json:"files_mapped"`
	CurrentTitle       string    `json:"current_title,omitempty"`
	Phase              string    `json:"current_phase"`
	MetadataGeneration string    `json:"metadata_generation"`
	MetadataBytes      int64     `json:"metadata_bytes_generated"`
	ExtentCount        int       `json:"extent_count"`
	Validation         string    `json:"validation_state"`
	ValidationFiles    int       `json:"validation_files_processed"`
	ValidationTotal    int       `json:"validation_total_files"`
	ValidationBytes    int64     `json:"validation_bytes_hashed"`
	Started            time.Time `json:"started,omitempty"`
	Completed          time.Time `json:"completed,omitempty"`
	Error              string    `json:"error,omitempty"`
}

type ValidationReceipt struct {
	Schema                int       `json:"schema"`
	ValidatorSchema       int       `json:"validator_schema"`
	GenerationID          string    `json:"generation_id"`
	CatalogFingerprint    string    `json:"catalog_fingerprint"`
	SourceIdentitySetHash string    `json:"source_identity_set_hash"`
	Completed             time.Time `json:"completed_utc"`
	Result                string    `json:"result"`
}

type ValidationProgress struct {
	FilesCompleted int
	TotalFiles     int
	BytesHashed    int64
}

type LibraryManager struct {
	mu        sync.RWMutex
	root      string
	config    LibraryConfig
	progress  LibraryBuildProgress
	active    *LibraryManifest
	validated bool
	cancel    context.CancelFunc
	legacy    []string
}

func NewLibraryManager(root string, config LibraryConfig) (*LibraryManager, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	clean, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if config.SourceRoot != "" {
		config.SourceRoot, err = trustedRoot(config.SourceRoot)
		if err != nil {
			// Keep authoritative generation metadata available while a source
			// mount is offline. Fast generation validation will safely block
			// activation until the same source identity returns.
			config.SourceRoot, err = filepath.Abs(config.SourceRoot)
			if err != nil {
				return nil, fmt.Errorf("GameCube source root: %w", err)
			}
		}
	}
	if config.SavesRoot != "" {
		config.SavesRoot, err = filepath.Abs(config.SavesRoot)
		if err != nil {
			return nil, fmt.Errorf("GameCube save root: %w", err)
		}
	}
	if err = os.MkdirAll(filepath.Join(clean, "generations"), 0o700); err != nil {
		return nil, err
	}
	manager := &LibraryManager{
		root: clean, config: config, progress: LibraryBuildProgress{State: "Not built"},
	}
	entries, err := os.ReadDir(filepath.Join(clean, "generations"))
	if err != nil {
		return nil, err
	}
	manager.legacy = manager.detectLegacy(entries)
	if active, activeErr := manager.activeManaged(); activeErr == nil {
		validation := "pending"
		state := "Validating"
		phase := "Deep validation pending"
		if fastErr := manager.validateConfiguredSources(active); fastErr != nil {
			validation, state, phase = "blocked", "Source unavailable", "Source validation blocked"
		} else if receiptErr := validateReceipt(manager.root, active); receiptErr == nil {
			validation, state, phase = "validated", "Ready", "Ready"
			manager.validated = true
		}
		manager.progress = LibraryBuildProgress{
			State: state, GenerationID: active.GenerationID,
			GamesCompleted: active.TitleCount, TotalGames: active.TitleCount,
			DiscsCompleted: active.DiscCount, TotalDiscs: active.DiscCount,
			FilesMapped: active.MappedFileCount, Phase: phase,
			MetadataGeneration: phase, ExtentCount: active.MappedExtentCount,
			Validation: validation, Completed: active.Created,
		}
		if info, statErr := os.Stat(active.MetadataPath); statErr == nil {
			manager.progress.MetadataBytes = info.Size()
		}
		manager.active = &active
	} else if !errors.Is(activeErr, os.ErrNotExist) {
		manager.progress = LibraryBuildProgress{
			State: "Failed", Phase: "Fast generation validation failed",
			MetadataGeneration: "Fast generation validation failed",
			Validation:         "failed", Error: boundedError(activeErr),
		}
	}
	return manager, nil
}

func trustedRoot(root string) (string, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	return filepath.Abs(real)
}

func (manager *LibraryManager) Root() string { return manager.root }

func (manager *LibraryManager) ConfigureSaves(selection SaveSelection) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.cancel != nil {
		return errors.New("GameCube library build is active")
	}
	next := manager.config
	next.Mode = selection.Mode
	next.CardSize = selection.CardSize
	next.SharedCardName = selection.SharedCardName
	next.AutoCreateCards = selection.AutomaticCreation
	next.MaxSaveBackups = selection.MaximumRetainedBackups
	if err := next.Validate(); err != nil {
		return err
	}
	manager.config = next
	return nil
}

func (manager *LibraryManager) EnsureSaveObjects(games []Game) ([]SaveObject, error) {
	manager.mu.RLock()
	config := manager.config
	manager.mu.RUnlock()
	objects, err := PlanSaveObjects(
		config.Mode, games, config.SharedCardName, config.CardSize)
	if err != nil {
		return nil, err
	}
	// This is the explicit administrator "Create card" operation, so it
	// intentionally creates missing cards even when automatic creation is off.
	if err = EnsureSaveObjects(config.SavesRoot, objects, true,
		config.Application, "unassigned"); err != nil {
		return nil, err
	}
	return objects, nil
}

func (manager *LibraryManager) LegacyGenerations() []string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return append([]string(nil), manager.legacy...)
}

func (manager *LibraryManager) Progress() LibraryBuildProgress {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.progress
}

func (manager *LibraryManager) ValidatedSummary() (LibraryManifest, bool) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.active == nil || !manager.validated {
		return LibraryManifest{}, false
	}
	return *manager.active, true
}

func (manager *LibraryManager) Cancel() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.cancel == nil {
		return false
	}
	manager.cancel()
	return true
}

func (manager *LibraryManager) StartActiveValidation(ctx context.Context) error {
	manager.mu.Lock()
	if manager.cancel != nil {
		manager.mu.Unlock()
		return errors.New("GameCube library operation is already running")
	}
	manifest, err := manager.activeFast()
	if err != nil {
		manager.mu.Unlock()
		return err
	}
	validationContext, cancel := context.WithCancel(ctx)
	manager.cancel = cancel
	manager.progress.State = "Validating"
	manager.progress.Phase = "Deep source validation"
	manager.progress.MetadataGeneration = "Deep source validation"
	manager.progress.Validation = "validating"
	manager.progress.ValidationFiles = 0
	manager.progress.ValidationTotal = len(manifest.Files)
	manager.progress.ValidationBytes = 0
	manager.progress.Started = time.Now().UTC()
	manager.progress.Completed = time.Time{}
	manager.progress.Error = ""
	manager.mu.Unlock()

	go func() {
		validateErr := ValidateLibraryManifestDeep(validationContext,
			manager.root, manifest, func(update ValidationProgress) {
				manager.mu.Lock()
				manager.progress.ValidationFiles = update.FilesCompleted
				manager.progress.ValidationTotal = update.TotalFiles
				manager.progress.ValidationBytes = update.BytesHashed
				manager.mu.Unlock()
			})
		if validateErr == nil {
			validateErr = writeValidationReceipt(manager.root, manifest)
		}
		manager.mu.Lock()
		defer manager.mu.Unlock()
		manager.cancel = nil
		manager.progress.Completed = time.Now().UTC()
		if validateErr != nil {
			manager.validated = false
			if errors.Is(validateErr, context.Canceled) {
				manager.progress.State = "Canceled"
				manager.progress.Error = "validation canceled"
			} else {
				manager.progress.State = "Failed"
				manager.progress.Error = boundedError(validateErr)
			}
			manager.progress.Validation = "failed"
			manager.progress.Phase = "Deep validation failed"
			manager.progress.MetadataGeneration = "Deep validation failed"
			return
		}
		manager.progress.State = "Ready"
		manager.progress.GenerationID = manifest.GenerationID
		manager.progress.Validation = "validated"
		manager.progress.Phase = "Ready"
		manager.progress.MetadataGeneration = "Ready"
		manager.active = &manifest
		manager.validated = true
	}()
	return nil
}

func (manager *LibraryManager) StartBuild(ctx context.Context, games []Game) error {
	manager.mu.Lock()
	if manager.cancel != nil {
		manager.mu.Unlock()
		return errors.New("GameCube library build is already running")
	}
	buildContext, cancel := context.WithCancel(ctx)
	manager.cancel = cancel
	totalDiscs := 0
	for _, game := range games {
		totalDiscs += len(game.Discs)
	}
	manager.progress = LibraryBuildProgress{
		State: "Building", TotalGames: len(games), TotalDiscs: totalDiscs,
		Phase: "Fingerprinting", MetadataGeneration: "Fingerprinting",
		Validation: "pending", Started: time.Now().UTC(),
	}
	manager.mu.Unlock()
	go func() {
		manifest, err := manager.build(buildContext, games)
		manager.mu.Lock()
		defer manager.mu.Unlock()
		manager.cancel = nil
		manager.progress.Completed = time.Now().UTC()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				manager.progress.State = "Canceled"
				manager.progress.Error = "build canceled"
			} else {
				manager.progress.State = "Failed"
				manager.progress.Error = boundedError(err)
			}
			return
		}
		manager.progress.State = "Ready"
		manager.progress.GenerationID = manifest.GenerationID
		manager.progress.GamesCompleted = manifest.TitleCount
		manager.progress.DiscsCompleted = manifest.DiscCount
		manager.progress.FilesMapped = manifest.MappedFileCount
		manager.progress.CurrentTitle = ""
		manager.progress.Phase = "Ready"
		manager.progress.MetadataGeneration = "Ready"
		manager.progress.Validation = "validated"
		manager.active = &manifest
		manager.validated = true
	}()
	return nil
}

func boundedError(err error) string {
	value := err.Error()
	for _, safe := range []string{
		"requires", "capacity", "overflow", "no validated GameCube", "FAT32",
		"build canceled", "source", "duplicate", "symlink", "no-copy",
	} {
		if strings.Contains(value, safe) {
			if len(value) > 240 {
				return value[:240]
			}
			return value
		}
	}
	return "GameCube library build failed during source indexing or metadata generation"
}

func (manager *LibraryManager) setProgress(title, phase string, games, discs, files int) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.progress.CurrentTitle = title
	manager.progress.GamesCompleted = games
	manager.progress.DiscsCompleted = discs
	manager.progress.FilesMapped = files
	manager.progress.Phase = phase
	manager.progress.MetadataGeneration = phase
}

func (manager *LibraryManager) setLayoutProgress(phase string, metadataBytes int64, extents int) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.progress.Phase = phase
	manager.progress.MetadataGeneration = phase
	manager.progress.MetadataBytes = metadataBytes
	manager.progress.ExtentCount = extents
}

func (manager *LibraryManager) Build(ctx context.Context, games []Game) (LibraryManifest, error) {
	return manager.build(ctx, games)
}

func (manager *LibraryManager) build(ctx context.Context, games []Game) (LibraryManifest, error) {
	if len(games) == 0 {
		return LibraryManifest{}, errors.New("no validated GameCube titles are available")
	}
	if manager.config.SourceRoot == "" {
		return LibraryManifest{}, errors.New("configured read-only GameCube source root is required")
	}
	hashed := make([]Game, len(games))
	totalDiscs := 0
	var payloadBytes int64
	for index, game := range games {
		if err := ctx.Err(); err != nil {
			return LibraryManifest{}, err
		}
		if game.Validation != "valid" || len(game.Discs) == 0 || len(game.Discs) > 2 {
			return LibraryManifest{}, fmt.Errorf("%s is not a validated one- or two-disc title", game.ID)
		}
		if game.DiscCount != len(game.Discs) {
			return LibraryManifest{}, fmt.Errorf("%s has an incomplete disc set", game.ID)
		}
		for discIndex, disc := range game.Discs {
			if disc.Number != byte(discIndex) || disc.ID != game.ID ||
				disc.Revision != game.Revision || disc.Validation != "valid" ||
				(disc.Format != "iso" && disc.Format != "gcm" &&
					disc.Format != "ciso" && disc.Format != "fst") {
				return LibraryManifest{}, fmt.Errorf("%s has inconsistent disc metadata", game.ID)
			}
		}
		var err error
		hashed[index], err = hashGameSources(game)
		if err != nil {
			return LibraryManifest{}, err
		}
		for _, disc := range hashed[index].Discs {
			if err = validateDiscSource(manager.config.SourceRoot, disc); err != nil {
				return LibraryManifest{}, err
			}
			if disc.Format != "fst" && disc.PhysicalSize > fat32MaximumFileSize {
				return LibraryManifest{}, fmt.Errorf("%s disc %d exceeds the FAT32 file limit", game.ID, disc.Number+1)
			}
			if payloadBytes > math.MaxInt64-disc.PhysicalSize {
				return LibraryManifest{}, errors.New("GameCube library size overflow")
			}
			payloadBytes += disc.PhysicalSize
			totalDiscs++
		}
		manager.setProgress(game.Title, "Fingerprinting", index+1, totalDiscs, 0)
	}
	sort.Slice(hashed, func(i, j int) bool {
		if hashed[i].ID == hashed[j].ID {
			return hashed[i].Revision < hashed[j].Revision
		}
		return hashed[i].ID < hashed[j].ID
	})
	fingerprint := manager.catalogFingerprint(hashed)
	virtualSize, err := CalculateLibrarySize(payloadBytes, manager.config)
	if err != nil {
		return LibraryManifest{}, err
	}
	generationID := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + fingerprint[:16]
	generations := filepath.Join(manager.root, "generations")
	staging := filepath.Join(generations, ".building-"+generationID)
	final := filepath.Join(generations, generationID)
	if err = os.Mkdir(staging, 0o700); err != nil {
		return LibraryManifest{}, err
	}
	defer os.RemoveAll(staging)
	manager.setProgress("", "Planning filesystem", len(hashed), totalDiscs, 0)
	saveObjects, err := PlanSaveObjects(manager.config.Mode, hashed,
		manager.config.SharedCardName, manager.config.CardSize)
	if err != nil {
		return LibraryManifest{}, err
	}
	if err = EnsureSaveObjects(manager.config.SavesRoot, saveObjects,
		manager.config.AutoCreateCards, manager.config.Application, generationID); err != nil {
		return LibraryManifest{}, err
	}
	files, titles, err := manager.mapFiles(ctx, hashed)
	if err != nil {
		return LibraryManifest{}, err
	}
	for _, object := range saveObjects {
		files = append(files, fat32virtual.File{
			VirtualPath: object.VirtualPath, LogicalSize: object.CardSize,
			Writable: true, SaveObjectID: object.ID, CardSize: object.CardSize,
			GenerationID: generationID, Format: "save", GameID: object.GameID,
		})
	}
	manager.setProgress("", "Generating FAT metadata", len(hashed), totalDiscs, len(files))
	layout, metadata, err := fat32virtual.Build(virtualSize, "WIIBRIDGE", fingerprint, files)
	if err != nil {
		return LibraryManifest{}, err
	}
	files = layout.Files
	manager.setLayoutProgress("Generating FAT metadata", int64(len(metadata)),
		len(layout.SourceExtents))
	layoutPath := filepath.Join(staging, "layout.bin")
	metadataPath := filepath.Join(staging, "metadata.bin")
	layoutData, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		return LibraryManifest{}, err
	}
	if err = os.WriteFile(layoutPath, append(layoutData, '\n'), 0o600); err != nil {
		return LibraryManifest{}, err
	}
	if err = os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		return LibraryManifest{}, err
	}
	checksums := map[string]string{
		"layout_sha256":          hashBytes(layoutData),
		"metadata_sha256":        layout.MetadataHash,
		"extent_map_sha256":      layout.ExtentMapHash,
		"save_extent_map_sha256": layout.SaveExtentHash,
		"layout_checksum":        layout.LayoutChecksum,
	}
	checksumData, _ := json.MarshalIndent(map[string]any{
		"schema": LibrarySchema, "checksums": checksums,
	}, "", "  ")
	if err = os.WriteFile(filepath.Join(staging, "checksums.json"),
		append(checksumData, '\n'), 0o600); err != nil {
		return LibraryManifest{}, err
	}
	manifest := LibraryManifest{
		Schema: LibrarySchema, GenerationID: generationID, Created: time.Now().UTC(),
		VolumeSize: virtualSize, Filesystem: "fat32", Geometry: layout.Geometry,
		ClusterSize: layout.Geometry.ClusterSize, Mode: manager.config.Mode,
		ReadOnly:   manager.config.Mode == MemoryCardPhysical,
		TitleCount: len(hashed), DiscCount: totalDiscs, MappedFileCount: len(files),
		MappedExtentCount: len(layout.SourceExtents), CatalogFingerprint: fingerprint,
		MetadataHash: layout.MetadataHash, ExtentMapHash: layout.ExtentMapHash,
		LayoutPath:   filepath.Join(final, "layout.bin"),
		MetadataPath: filepath.Join(final, "metadata.bin"),
		LibraryRoot:  manager.config.SourceRoot, Titles: titles, Files: files, Complete: true,
		SaveOverlayVersion: func() int {
			if manager.config.Mode.IsLibraryEmulated() {
				return SaveOverlayFormatVersion
			}
			return 0
		}(),
		SaveObjects: saveObjects, SaveExtentCount: len(layout.SaveExtents),
		SaveExtentHash: layout.SaveExtentHash, LayoutChecksum: layout.LayoutChecksum,
		MaxSaveBackups: manager.config.MaxSaveBackups, Application: manager.config.Application,
	}
	staged := manifest
	staged.LayoutPath, staged.MetadataPath = layoutPath, metadataPath
	manager.setProgress("", "Validating generation", len(hashed), totalDiscs, len(files))
	if err = ValidateLibraryManifest(manager.root, staged); err != nil {
		return LibraryManifest{}, err
	}
	if err = writeValidationReceipt(manager.root, staged); err != nil {
		return LibraryManifest{}, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return LibraryManifest{}, err
	}
	if err = os.WriteFile(filepath.Join(staging, "manifest.json"), append(data, '\n'), 0o600); err != nil {
		return LibraryManifest{}, err
	}
	if err = syncDirectory(staging); err != nil {
		return LibraryManifest{}, err
	}
	if err = os.Rename(staging, final); err != nil {
		return LibraryManifest{}, err
	}
	if err = syncDirectory(generations); err != nil {
		return LibraryManifest{}, err
	}
	if err = manager.promote(manifest); err != nil {
		return LibraryManifest{}, err
	}
	_ = manager.prune(manifest.GenerationID)
	return manifest, nil
}

func (manager *LibraryManager) mapFiles(ctx context.Context, games []Game) (
	[]fat32virtual.File, []LibraryTitle, error,
) {
	var files []fat32virtual.File
	var titles []LibraryTitle
	seen := make(map[string]struct{})
	discsCompleted := 0
	for gameIndex, game := range games {
		firstMapped := len(files)
		output, err := libraryOutputDir(game)
		if err != nil {
			return nil, nil, err
		}
		outputDir := "/games/" + output
		prepared := sha256.New()
		for discIndex, disc := range game.Discs {
			if err = ctx.Err(); err != nil {
				return nil, nil, err
			}
			fmt.Fprint(prepared, disc.SHA256)
			if disc.Format == "fst" {
				if len(game.Discs) != 1 {
					return nil, nil, errors.New("two-disc extracted FST sets are unsupported")
				}
				mapped, mapErr := mapFST(manager.config.SourceRoot, outputDir, disc, game)
				if mapErr != nil {
					return nil, nil, mapErr
				}
				files = append(files, mapped...)
			} else {
				identity, identityErr := sourceIdentity(disc.SourcePath, disc.SHA256)
				if identityErr != nil {
					return nil, nil, identityErr
				}
				files = append(files, fat32virtual.File{
					VirtualPath: outputDir + "/" + runtimeName(disc, discIndex == 1),
					SourcePath:  disc.SourcePath, LogicalSize: disc.PhysicalSize,
					SourceSize: disc.PhysicalSize, Identity: identity,
					GameID: game.ID, Revision: game.Revision,
					DiscNumber: disc.Number, Format: disc.Format,
				})
			}
			discsCompleted++
			manager.setProgress(game.Title, "Mapping sources", gameIndex,
				discsCompleted, len(files))
		}
		for _, file := range files[firstMapped:] {
			key := strings.ToLower(file.VirtualPath)
			if _, exists := seen[key]; exists {
				return nil, nil, fmt.Errorf("duplicate case-insensitive FAT path %q", file.VirtualPath)
			}
			seen[key] = struct{}{}
		}
		titles = append(titles, LibraryTitle{
			Title: game.Title, ID: game.ID, Revision: game.Revision,
			Region: game.Region, Format: game.Format, DiscCount: len(game.Discs),
			OutputDir: outputDir, PreparedID: hex.EncodeToString(prepared.Sum(nil)),
		})
		manager.setProgress(game.Title, "Mapping sources", gameIndex+1,
			discsCompleted, len(files))
	}
	return files, titles, nil
}

func mapFST(root, output string, disc Disc, game Game) ([]fat32virtual.File, error) {
	fstRoot, err := trustedRoot(disc.SourcePath)
	if err != nil {
		return nil, err
	}
	if err = within(root, fstRoot); err != nil {
		return nil, err
	}
	var files []fat32virtual.File
	err = filepath.WalkDir(fstRoot, func(source string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if source == fstRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink forbidden in extracted FST")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("special file forbidden in extracted FST")
		}
		if err := within(fstRoot, source); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > fat32MaximumFileSize {
			return fmt.Errorf("extracted FST file %s exceeds the FAT32 file limit", entry.Name())
		}
		sum, err := hashFile(source)
		if err != nil {
			return err
		}
		identity, err := sourceIdentity(source, sum)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(fstRoot, source)
		files = append(files, fat32virtual.File{
			VirtualPath: output + "/" + filepath.ToSlash(relative),
			SourcePath:  source, LogicalSize: info.Size(), SourceSize: info.Size(),
			Identity: identity, GameID: game.ID, Revision: game.Revision,
			DiscNumber: disc.Number, Format: "fst", FSTRoot: fstRoot,
			FSTTreeSHA256: disc.SHA256,
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].VirtualPath < files[j].VirtualPath })
	return files, err
}

func validateDiscSource(root string, disc Disc) error {
	if err := within(root, disc.SourcePath); err != nil {
		return err
	}
	info, err := os.Lstat(disc.SourcePath)
	if err != nil {
		return err
	}
	if disc.Format == "fst" {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid extracted FST source root")
		}
		return nil
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != disc.PhysicalSize {
		return errors.New("GameCube source size or type changed")
	}
	return nil
}

func sourceIdentity(path, sum string) (fat32virtual.Identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fat32virtual.Identity{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fat32virtual.Identity{}, errors.New("GameCube source is not a regular file")
	}
	identity := fat32virtual.Identity{
		Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano(), SHA256: sum,
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		identity.Device, identity.Inode = uint64(stat.Dev), stat.Ino
	}
	return identity, nil
}

func CalculateLibrarySize(payloadBytes int64, config LibraryConfig) (int64, error) {
	if err := config.Validate(); err != nil {
		return 0, err
	}
	if payloadBytes < 0 {
		return 0, errors.New("negative GameCube payload size")
	}
	if payloadBytes > math.MaxInt64/100 {
		return 0, errors.New("GameCube library size overflow")
	}
	headroom := payloadBytes * int64(config.HeadroomPercent) / 100
	saveReserve := config.SaveReserveMiB << 20
	if payloadBytes > math.MaxInt64-headroom ||
		payloadBytes+headroom > math.MaxInt64-saveReserve-libraryMetadataReserve {
		return 0, errors.New("GameCube library size overflow")
	}
	size := payloadBytes + headroom + saveReserve + libraryMetadataReserve
	if size < libraryMinimumSize {
		size = libraryMinimumSize
	}
	if size > math.MaxInt64-(libraryAlignment-1) {
		return 0, errors.New("GameCube library size overflow")
	}
	size = (size + libraryAlignment - 1) / libraryAlignment * libraryAlignment
	if size/fat32virtual.SectorSize > math.MaxUint32 {
		return 0, errors.New("GameCube library exceeds MBR/FAT32 capacity")
	}
	if config.MaxVolumeGiB > 0 && size > config.MaxVolumeGiB<<30 {
		return 0, fmt.Errorf("GameCube library requires %d GiB; configured maximum is %d GiB",
			size>>30, config.MaxVolumeGiB)
	}
	return size, nil
}

func catalogFingerprint(games []Game) string {
	hash := sha256.New()
	for _, game := range games {
		fmt.Fprintf(hash, "%s\x00%d\x00%s", game.ID, game.Revision, game.Title)
		for _, disc := range game.Discs {
			fmt.Fprintf(hash, "\x00%d\x00%s\x00%d\x00%s",
				disc.Number, disc.Format, disc.PhysicalSize, disc.SHA256)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (manager *LibraryManager) catalogFingerprint(games []Game) string {
	base := catalogFingerprint(games)
	if manager.config.Mode == MemoryCardPhysical {
		return base
	}
	return hashBytes([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%d",
		base, manager.config.Mode, manager.config.CardSize,
		manager.config.SharedCardName, SaveOverlayFormatVersion)))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func libraryOutputDir(game Game) (string, error) {
	if game.ID == "" || strings.Contains(game.Title, "../") || strings.Contains(game.Title, `..\`) {
		return "", errors.New("unsafe GameCube title path")
	}
	title := volumeTitle(game)
	if game.Revision > 0 {
		title += fmt.Sprintf(" [Rev %d]", game.Revision)
	}
	base := strings.TrimSpace(strings.SplitN(title, " ", 2)[0])
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4",
		"COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3",
		"LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		title = "_" + title
	}
	if !strings.Contains(title, "["+game.ID+"]") {
		return "", errors.New("GameCube output directory lacks game ID")
	}
	return title, nil
}

func (manager *LibraryManager) promote(manifest LibraryManifest) error {
	pointer := struct {
		Schema       int    `json:"schema"`
		GenerationID string `json:"generation_id"`
	}{LibrarySchema, manifest.GenerationID}
	data, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(manager.root, ".active-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(append(data, '\n'))
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
	if err = os.Rename(tempName, filepath.Join(manager.root, "active.json")); err != nil {
		return err
	}
	return syncDirectory(manager.root)
}

func (manager *LibraryManager) Active() (LibraryManifest, error) {
	manifest, err := manager.activeFast()
	if err != nil {
		return LibraryManifest{}, err
	}
	if err = validateReceipt(manager.root, manifest); err != nil {
		return LibraryManifest{}, err
	}
	return manifest, nil
}

func (manager *LibraryManager) ManagedActive() (LibraryManifest, error) {
	return manager.activeManaged()
}

func (manager *LibraryManager) RecheckActive() error {
	manifest, err := manager.activeFast()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err != nil {
		manager.validated = false
		manager.progress.State = "Source unavailable"
		if errors.Is(err, ErrGameCubeSourceChanged) {
			manager.progress.State = "Source changed"
		}
		manager.progress.Validation = "blocked"
		manager.progress.Phase = "Source validation blocked"
		manager.progress.Error = boundedError(err)
		// An offline source does not invalidate the last successful validation.
		// Only a positively observed identity change revokes the receipt.
		if errors.Is(err, ErrGameCubeSourceChanged) {
			generationID := manifest.GenerationID
			if generationID == "" {
				generationID = manager.progress.GenerationID
			}
			receipt := validationReceiptPathForGeneration(manager.root, generationID)
			if receipt != "" {
				_ = os.Remove(receipt)
				_ = syncDirectory(filepath.Dir(receipt))
			}
		}
		return err
	}
	manager.active = &manifest
	manager.progress.GenerationID = manifest.GenerationID
	manager.progress.Error = ""
	if err = validateReceipt(manager.root, manifest); err != nil {
		manager.validated = false
		manager.progress.State = "Validating"
		manager.progress.Validation = "pending"
		manager.progress.Phase = "Deep validation pending"
		return err
	}
	manager.validated = true
	manager.progress.State = "Ready"
	manager.progress.Validation = "validated"
	manager.progress.Phase = "Ready"
	return nil
}

func validationReceiptPathForGeneration(root, generation string) string {
	if !safeGenerationID(generation) {
		return ""
	}
	return filepath.Join(root, "generations", generation, "validation.json")
}

func (manager *LibraryManager) validateConfiguredSources(manifest LibraryManifest) error {
	if manager.config.SourceRoot != "" && filepath.Clean(manifest.LibraryRoot) != manager.config.SourceRoot {
		return fmt.Errorf("%w: configured library path changed", ErrGameCubeSourceChanged)
	}
	return ValidateLibraryManifestFast(manager.root, manifest)
}

func (manager *LibraryManager) activeFast() (LibraryManifest, error) {
	manifest, err := manager.activeManaged()
	if err != nil {
		return LibraryManifest{}, err
	}
	if err = manager.validateConfiguredSources(manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func (manager *LibraryManager) activeManaged() (LibraryManifest, error) {
	data, err := os.ReadFile(filepath.Join(manager.root, "active.json"))
	if err != nil {
		return LibraryManifest{}, err
	}
	var pointer struct {
		Schema       int    `json:"schema"`
		GenerationID string `json:"generation_id"`
	}
	if err = json.Unmarshal(data, &pointer); err != nil {
		return LibraryManifest{}, err
	}
	if pointer.Schema != LibrarySchema || !safeGenerationID(pointer.GenerationID) {
		return LibraryManifest{}, errors.New("invalid GameCube active-generation pointer")
	}
	return LoadLibraryManaged(manager.root,
		filepath.Join(manager.root, "generations", pointer.GenerationID, "manifest.json"))
}

func safeGenerationID(value string) bool {
	return value != "" && filepath.Base(value) == value &&
		!strings.Contains(value, "..") && !strings.ContainsAny(value, `/\`)
}

func LoadAndValidateLibrary(root, manifestPath string) (LibraryManifest, error) {
	manifest, err := loadLibraryManifest(root, manifestPath)
	if err != nil {
		return LibraryManifest{}, err
	}
	if err = ValidateLibraryManifest(root, manifest); err != nil {
		return LibraryManifest{}, err
	}
	return manifest, nil
}

func LoadLibraryFast(root, manifestPath string) (LibraryManifest, error) {
	manifest, err := loadLibraryManifest(root, manifestPath)
	if err != nil {
		return LibraryManifest{}, err
	}
	if err = ValidateLibraryManifestFast(root, manifest); err != nil {
		return LibraryManifest{}, err
	}
	return manifest, nil
}

func LoadLibraryManaged(root, manifestPath string) (LibraryManifest, error) {
	manifest, err := loadLibraryManifest(root, manifestPath)
	if err != nil {
		return LibraryManifest{}, err
	}
	if err = ValidateLibraryManifestManaged(root, manifest); err != nil {
		return LibraryManifest{}, err
	}
	return manifest, nil
}

func loadLibraryManifest(root, manifestPath string) (LibraryManifest, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return LibraryManifest{}, err
	}
	manifestAbsolute, err := filepath.Abs(manifestPath)
	if err != nil || !strings.HasPrefix(manifestAbsolute, rootAbsolute+string(os.PathSeparator)) {
		return LibraryManifest{}, errors.New("GameCube library manifest escapes managed storage")
	}
	info, err := os.Lstat(manifestAbsolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return LibraryManifest{}, errors.New("invalid GameCube library manifest")
	}
	data, err := os.ReadFile(manifestAbsolute)
	if err != nil {
		return LibraryManifest{}, err
	}
	var manifest LibraryManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return LibraryManifest{}, err
	}
	return manifest, nil
}

func ValidateLibraryManifest(root string, manifest LibraryManifest) error {
	return validateLibraryManifest(context.Background(), root, manifest, true, true, nil)
}

func ValidateLibraryManifestFast(root string, manifest LibraryManifest) error {
	return validateLibraryManifest(context.Background(), root, manifest, false, true, nil)
}

func ValidateLibraryManifestManaged(root string, manifest LibraryManifest) error {
	return validateLibraryManifest(context.Background(), root, manifest, false, false, nil)
}

func ValidateLibraryManifestDeep(ctx context.Context, root string,
	manifest LibraryManifest, progress func(ValidationProgress),
) error {
	return validateLibraryManifest(ctx, root, manifest, true, true, progress)
}

func validateLibraryManifest(ctx context.Context, root string,
	manifest LibraryManifest, deep, checkSources bool, progress func(ValidationProgress),
) error {
	validMode := manifest.Mode == MemoryCardPhysical || manifest.Mode.IsLibraryEmulated()
	validWriteMode := (manifest.Mode == MemoryCardPhysical && manifest.ReadOnly &&
		manifest.SaveOverlayVersion == 0 && len(manifest.SaveObjects) == 0 &&
		manifest.SaveExtentCount == 0) ||
		(manifest.Mode.IsLibraryEmulated() && !manifest.ReadOnly &&
			manifest.SaveOverlayVersion == SaveOverlayFormatVersion &&
			len(manifest.SaveObjects) > 0 && manifest.SaveExtentCount > 0)
	if manifest.Schema != LibrarySchema || !manifest.Complete ||
		!safeGenerationID(manifest.GenerationID) || manifest.Filesystem != "fat32" ||
		!validMode || !validWriteMode {
		return errors.New("stale or incomplete no-copy GameCube library manifest")
	}
	if manifest.TitleCount == 0 || manifest.TitleCount != len(manifest.Titles) ||
		manifest.MappedFileCount == 0 || manifest.MappedFileCount != len(manifest.Files) {
		return errors.New("GameCube library manifest count mismatch")
	}
	layoutData, err := readManagedFile(root, manifest.LayoutPath)
	if err != nil {
		return err
	}
	metadata, err := readManagedFile(root, manifest.MetadataPath)
	if err != nil {
		return err
	}
	var layout fat32virtual.Layout
	if err = json.Unmarshal(layoutData, &layout); err != nil {
		return err
	}
	if layout.Schema != LibrarySchema || layout.VirtualSize != manifest.VolumeSize ||
		layout.MetadataHash != manifest.MetadataHash ||
		layout.ExtentMapHash != manifest.ExtentMapHash ||
		len(layout.SourceExtents) != manifest.MappedExtentCount ||
		hashBytes(metadata) != manifest.MetadataHash {
		return errors.New("GameCube no-copy layout checksum or geometry mismatch")
	}
	if manifest.Mode.IsLibraryEmulated() &&
		(len(layout.SaveExtents) != manifest.SaveExtentCount ||
			layout.SaveExtentHash != manifest.SaveExtentHash ||
			layout.LayoutChecksum != manifest.LayoutChecksum) {
		return errors.New("GameCube writable save extent map mismatch")
	}
	saveObjects := make(map[string]SaveObject, len(manifest.SaveObjects))
	for _, object := range manifest.SaveObjects {
		if _, duplicate := saveObjects[object.ID]; duplicate {
			return errors.New("GameCube writable save object map is ambiguous")
		}
		saveObjects[object.ID] = object
	}
	for _, extent := range layout.SaveExtents {
		object, ok := saveObjects[extent.SaveObjectID]
		if extent.GenerationID != manifest.GenerationID ||
			extent.LayoutChecksum != manifest.LayoutChecksum || !ok ||
			extent.CardSize != object.CardSize || extent.Length != object.CardSize {
			return errors.New("GameCube writable save extent generation mismatch")
		}
		delete(saveObjects, extent.SaveObjectID)
	}
	if manifest.Mode.IsLibraryEmulated() && len(saveObjects) != 0 {
		return errors.New("GameCube writable save object has no trusted extent")
	}
	if len(layout.Files) != len(manifest.Files) {
		return errors.New("GameCube virtual file map mismatch")
	}
	if !reflect.DeepEqual(layout.Files, manifest.Files) {
		return errors.New("GameCube manifest and layout file maps differ")
	}
	checksumData, err := readManagedFile(root,
		filepath.Join(filepath.Dir(manifest.LayoutPath), "checksums.json"))
	if err != nil {
		return err
	}
	var checksumDocument struct {
		Schema    int               `json:"schema"`
		Checksums map[string]string `json:"checksums"`
	}
	if err = json.Unmarshal(checksumData, &checksumDocument); err != nil ||
		checksumDocument.Schema != LibrarySchema ||
		checksumDocument.Checksums["layout_sha256"] !=
			hashBytes(bytes.TrimSuffix(layoutData, []byte{'\n'})) ||
		checksumDocument.Checksums["metadata_sha256"] != manifest.MetadataHash ||
		checksumDocument.Checksums["extent_map_sha256"] != manifest.ExtentMapHash {
		return errors.New("GameCube generation checksum document mismatch")
	}
	if manifest.Mode.IsLibraryEmulated() &&
		(checksumDocument.Checksums["save_extent_map_sha256"] != manifest.SaveExtentHash ||
			checksumDocument.Checksums["layout_checksum"] != manifest.LayoutChecksum) {
		return errors.New("GameCube save extent checksum document mismatch")
	}
	var saveValidator fat32virtual.SaveStore
	if manifest.Mode.IsLibraryEmulated() {
		saveValidator = validationSaveStore{}
	}
	checkBackend, err := fat32virtual.OpenWithOptions(layout, metadata,
		fat32virtual.OpenOptions{CacheLimit: 1, SaveStore: saveValidator})
	if err != nil {
		return err
	}
	if err = checkBackend.Close(); err != nil {
		return err
	}
	rootAbsolute, err := filepath.Abs(manifest.LibraryRoot)
	if checkSources {
		rootAbsolute, err = trustedRoot(manifest.LibraryRoot)
	}
	if err != nil || manifest.LibraryRoot == "" {
		return errors.New("invalid GameCube source root metadata")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	checkedTrees := make(map[string]struct{})
	var bytesHashed int64
	for index, file := range manifest.Files {
		if err = ctx.Err(); err != nil {
			return err
		}
		key := strings.ToLower(filepath.ToSlash(file.VirtualPath))
		if _, exists := seen[key]; exists {
			return errors.New("duplicate GameCube virtual path")
		}
		seen[key] = struct{}{}
		if file.Writable {
			if !manifest.Mode.IsLibraryEmulated() || file.Format != "save" ||
				file.SaveObjectID == "" || file.SourcePath != "" ||
				file.LogicalSize != file.CardSize || file.CardSize <= 0 ||
				file.GenerationID != manifest.GenerationID ||
				file.VirtualOffset < 0 || file.AllocatedSize < file.LogicalSize ||
				file.VirtualOffset+file.AllocatedSize > manifest.VolumeSize {
				return errors.New("invalid GameCube writable save file metadata")
			}
			if progress != nil {
				progress(ValidationProgress{
					FilesCompleted: index + 1, TotalFiles: len(manifest.Files),
					BytesHashed: bytesHashed,
				})
			}
			continue
		}
		if !validID.MatchString(file.GameID) || file.DiscNumber > 1 ||
			(file.Format != "iso" && file.Format != "gcm" &&
				file.Format != "ciso" && file.Format != "fst") ||
			file.LogicalSize < 0 || file.SourceOffset < 0 ||
			file.SourceOffset+file.LogicalSize > file.SourceSize ||
			file.VirtualOffset < 0 || file.AllocatedSize < file.LogicalSize ||
			file.VirtualOffset+file.AllocatedSize > manifest.VolumeSize {
			return errors.New("invalid GameCube virtual file metadata")
		}
		if err = within(rootAbsolute, file.SourcePath); err != nil {
			return err
		}
		if file.Format == "fst" {
			if file.FSTRoot == "" || file.FSTTreeSHA256 == "" {
				return errors.New("missing extracted FST tree identity")
			}
			fstRoot, rootErr := filepath.Abs(file.FSTRoot)
			if checkSources {
				fstRoot, rootErr = trustedRoot(file.FSTRoot)
			}
			if rootErr != nil {
				return rootErr
			}
			if err = within(rootAbsolute, fstRoot); err != nil {
				return err
			}
			if err = within(fstRoot, file.SourcePath); err != nil {
				return err
			}
			if _, done := checkedTrees[fstRoot]; checkSources && deep && !done {
				treeHash, _, treeErr := hashTree(fstRoot)
				if treeErr != nil || treeHash != file.FSTTreeSHA256 {
					return errors.New("extracted FST tree changed")
				}
				checkedTrees[fstRoot] = struct{}{}
			}
		} else if file.FSTRoot != "" || file.FSTTreeSHA256 != "" {
			return errors.New("unexpected extracted FST identity")
		}
		if !checkSources {
			if progress != nil {
				progress(ValidationProgress{
					FilesCompleted: index + 1, TotalFiles: len(manifest.Files),
					BytesHashed: bytesHashed,
				})
			}
			continue
		}
		identity, identityErr := sourceIdentity(file.SourcePath, file.Identity.SHA256)
		if identityErr != nil {
			return fmt.Errorf("%w: %v", ErrGameCubeSourceUnavailable, identityErr)
		}
		if identity.Size != file.Identity.Size ||
			identity.ModTimeUnixNano != file.Identity.ModTimeUnixNano ||
			identity.Device != file.Identity.Device || identity.Inode != file.Identity.Inode {
			return ErrGameCubeSourceChanged
		}
		if deep && file.Format != "fst" {
			sum, hashErr := hashFile(file.SourcePath)
			if hashErr != nil {
				return fmt.Errorf("%w: %v", ErrGameCubeSourceUnavailable, hashErr)
			}
			if sum != file.Identity.SHA256 {
				return fmt.Errorf("%w: source fingerprint changed", ErrGameCubeSourceChanged)
			}
			bytesHashed += file.LogicalSize
		} else if deep {
			// hashTree reads every FST payload once. Account for each mapped
			// file as progress advances without hashing the tree repeatedly.
			bytesHashed += file.LogicalSize
		}
		if progress != nil {
			progress(ValidationProgress{
				FilesCompleted: index + 1, TotalFiles: len(manifest.Files),
				BytesHashed: bytesHashed,
			})
		}
	}
	return nil
}

func sourceIdentitySetHash(manifest LibraryManifest) string {
	files := append([]fat32virtual.File(nil), manifest.Files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].VirtualPath < files[j].VirtualPath
	})
	hash := sha256.New()
	for _, file := range files {
		if file.Writable {
			continue
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%s\n",
			file.VirtualPath, file.SourcePath, file.Identity.Size,
			file.Identity.ModTimeUnixNano, file.Identity.Device,
			file.Identity.Inode, file.Identity.SHA256)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type validationSaveStore struct{}

func (validationSaveStore) ReadSaveAt(string, []byte, int64) (int, error) {
	return 0, errors.New("validation save store does not serve reads")
}
func (validationSaveStore) WriteSaveAt(string, []byte, int64) (int, error) {
	return 0, errors.New("validation save store does not serve writes")
}
func (validationSaveStore) Sync() error  { return nil }
func (validationSaveStore) Close() error { return nil }

func validationReceiptPath(manifest LibraryManifest) string {
	return filepath.Join(filepath.Dir(manifest.LayoutPath), "validation.json")
}

func writeValidationReceipt(root string, manifest LibraryManifest) error {
	receipt := ValidationReceipt{
		Schema: validationReceiptSchema, ValidatorSchema: LibrarySchema,
		GenerationID:          manifest.GenerationID,
		CatalogFingerprint:    manifest.CatalogFingerprint,
		SourceIdentitySetHash: sourceIdentitySetHash(manifest),
		Completed:             time.Now().UTC(), Result: "validated",
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	path := validationReceiptPath(manifest)
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil || !strings.HasPrefix(absolute, rootAbsolute+string(os.PathSeparator)) {
		return errors.New("GameCube validation receipt escapes managed storage")
	}
	temp := absolute + ".tmp"
	if err = os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err = os.Rename(temp, absolute); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(absolute))
}

func validateReceipt(root string, manifest LibraryManifest) error {
	data, err := readManagedFile(root, validationReceiptPath(manifest))
	if err != nil {
		return errors.New("GameCube deep validation receipt is unavailable")
	}
	var receipt ValidationReceipt
	if err = json.Unmarshal(data, &receipt); err != nil ||
		receipt.Schema != validationReceiptSchema ||
		receipt.ValidatorSchema != LibrarySchema ||
		receipt.GenerationID != manifest.GenerationID ||
		receipt.CatalogFingerprint != manifest.CatalogFingerprint ||
		receipt.SourceIdentitySetHash != sourceIdentitySetHash(manifest) ||
		receipt.Result != "validated" || receipt.Completed.IsZero() {
		return errors.New("GameCube deep validation receipt is stale")
	}
	return nil
}

func readManagedFile(root, name string) ([]byte, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(name)
	if err != nil || !strings.HasPrefix(absolute, rootAbsolute+string(os.PathSeparator)) {
		return nil, errors.New("GameCube generation file escapes managed storage")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid GameCube generation file")
	}
	return os.ReadFile(absolute)
}

func OpenLibraryBackend(root string, manifest LibraryManifest) (*fat32virtual.Backend, error) {
	return OpenLibraryBackendWithMetrics(root, manifest, nil)
}

func OpenLibraryBackendWithMetrics(root string, manifest LibraryManifest,
	metrics *perf.Registry,
) (*fat32virtual.Backend, error) {
	backend, _, err := OpenLibraryBackendAndSaveStore(root, manifest, metrics)
	return backend, err
}

func OpenLibraryBackendAndSaveStore(root string, manifest LibraryManifest,
	metrics *perf.Registry,
) (*fat32virtual.Backend, *SaveStore, error) {
	if err := ValidateLibraryManifestFast(root, manifest); err != nil {
		return nil, nil, err
	}
	if err := validateReceipt(root, manifest); err != nil {
		return nil, nil, err
	}
	layoutData, err := readManagedFile(root, manifest.LayoutPath)
	if err != nil {
		return nil, nil, err
	}
	metadata, err := readManagedFile(root, manifest.MetadataPath)
	if err != nil {
		return nil, nil, err
	}
	var layout fat32virtual.Layout
	if err = json.Unmarshal(layoutData, &layout); err != nil {
		return nil, nil, err
	}
	var saves *SaveStore
	var saveBackend fat32virtual.SaveStore
	if manifest.Mode.IsLibraryEmulated() {
		saveRoot := filepath.Join(filepath.Dir(root), "saves")
		if err = EnsureSaveObjects(saveRoot, manifest.SaveObjects, false,
			manifest.Application, manifest.GenerationID); err != nil {
			return nil, nil, err
		}
		saves, err = OpenSaveStore(SaveStoreConfig{
			Root: saveRoot, Application: manifest.Application,
			GenerationID: manifest.GenerationID, LayoutChecksum: manifest.LayoutChecksum,
			MaxBackups: manifest.MaxSaveBackups, Metrics: metrics,
		}, manifest.SaveObjects)
		if err != nil {
			return nil, nil, err
		}
		saveBackend = saves
	}
	backend, err := fat32virtual.OpenWithOptions(layout, metadata,
		fat32virtual.OpenOptions{
			CacheLimit: libraryFileCacheLimit, SaveStore: saveBackend, Metrics: metrics,
		})
	if err != nil && saves != nil {
		_ = saves.Close()
	}
	return backend, saves, err
}

func OpenLibrarySaveStore(root string, manifest LibraryManifest,
	metrics *perf.Registry,
) (*SaveStore, error) {
	if !manifest.Mode.IsLibraryEmulated() {
		return nil, errors.New("physical memory-card mode has no managed save overlay")
	}
	if err := ValidateLibraryManifestManaged(root, manifest); err != nil {
		return nil, err
	}
	saveRoot := filepath.Join(filepath.Dir(root), "saves")
	if err := EnsureSaveObjects(saveRoot, manifest.SaveObjects, false,
		manifest.Application, manifest.GenerationID); err != nil {
		return nil, err
	}
	return OpenSaveStore(SaveStoreConfig{
		Root: saveRoot, Application: manifest.Application,
		GenerationID: manifest.GenerationID, LayoutChecksum: manifest.LayoutChecksum,
		MaxBackups: manifest.MaxSaveBackups, Metrics: metrics,
	}, manifest.SaveObjects)
}

func (manager *LibraryManager) Current(games []Game) (bool, error) {
	active, err := manager.Active()
	if err != nil {
		return false, err
	}
	hashed := make([]Game, len(games))
	for index, game := range games {
		hashed[index], err = hashGameSources(game)
		if err != nil {
			return false, err
		}
	}
	sort.Slice(hashed, func(i, j int) bool {
		if hashed[i].ID == hashed[j].ID {
			return hashed[i].Revision < hashed[j].Revision
		}
		return hashed[i].ID < hashed[j].ID
	})
	return active.CatalogFingerprint == manager.catalogFingerprint(hashed), nil
}

func (manager *LibraryManager) detectLegacy(entries []os.DirEntry) []string {
	var legacy []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".building-") ||
			!safeGenerationID(entry.Name()) {
			continue
		}
		generation := filepath.Join(manager.root, "generations", entry.Name())
		imageInfo, imageErr := os.Lstat(filepath.Join(generation, "library.img"))
		if imageErr == nil && imageInfo.Mode().IsRegular() {
			legacy = append(legacy, entry.Name())
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(generation, "manifest.json"))
		if readErr == nil {
			var header struct {
				Schema int `json:"schema"`
			}
			if json.Unmarshal(data, &header) == nil && header.Schema == 1 {
				legacy = append(legacy, entry.Name())
			}
		}
	}
	sort.Strings(legacy)
	return legacy
}

func (manager *LibraryManager) prune(active string) error {
	entries, err := os.ReadDir(filepath.Join(manager.root, "generations"))
	if err != nil {
		return err
	}
	var complete []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".building-") ||
			entry.Name() == active {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(manager.root, "generations",
			entry.Name(), "manifest.json"))
		var header struct {
			Schema int `json:"schema"`
		}
		if readErr == nil && json.Unmarshal(data, &header) == nil &&
			header.Schema == LibrarySchema {
			complete = append(complete, entry)
		}
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].Name() > complete[j].Name() })
	for index, entry := range complete {
		if index < manager.config.Retention-1 {
			continue
		}
		_ = os.RemoveAll(filepath.Join(manager.root, "generations", entry.Name()))
	}
	return nil
}

func BackupLibraryMemoryCards(manifest LibraryManifest, saveRoot string, retain int) error {
	if !manifest.Mode.IsLibraryEmulated() {
		return nil
	}
	if retain < 1 {
		return errors.New("save backup retention must be positive")
	}
	if filepath.Base(saveRoot) == "save-backups" {
		saveRoot = filepath.Join(filepath.Dir(saveRoot), "saves")
	}
	store, err := OpenSaveStore(SaveStoreConfig{
		Root: saveRoot, Application: manifest.Application,
		GenerationID: manifest.GenerationID, LayoutChecksum: manifest.LayoutChecksum,
		MaxBackups: retain,
	}, manifest.SaveObjects)
	if err != nil {
		return err
	}
	defer store.Close()
	for _, object := range manifest.SaveObjects {
		if _, err = store.Backup(object.ID, "detach"); err != nil {
			return err
		}
	}
	return nil
}

func StorageReport(manifest LibraryManifest) (map[string]int64, error) {
	var sourceBytes int64
	for _, file := range manifest.Files {
		sourceBytes += file.LogicalSize
	}
	var metadataApparent, metadataAllocated int64
	generationFiles := []string{manifest.LayoutPath, manifest.MetadataPath,
		filepath.Join(filepath.Dir(manifest.LayoutPath), "manifest.json"),
		filepath.Join(filepath.Dir(manifest.LayoutPath), "checksums.json"),
		validationReceiptPath(manifest)}
	for _, name := range generationFiles {
		info, err := os.Stat(name)
		if err != nil {
			return nil, err
		}
		metadataApparent += info.Size()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			metadataAllocated += stat.Blocks * 512
		} else {
			metadataAllocated += info.Size()
		}
	}
	var overlayApparent, overlayAllocated int64
	overlay := filepath.Join(filepath.Dir(manifest.LayoutPath), "overlay.bin")
	if info, overlayErr := os.Lstat(overlay); overlayErr == nil &&
		info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		overlayApparent = info.Size()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			overlayAllocated = stat.Blocks * 512
		} else {
			overlayAllocated = info.Size()
		}
	} else if overlayErr != nil && !os.IsNotExist(overlayErr) {
		return nil, overlayErr
	}
	var legacyBytes int64
	generations := filepath.Dir(filepath.Dir(manifest.LayoutPath))
	entries, err := os.ReadDir(generations)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !safeGenerationID(entry.Name()) {
			continue
		}
		image := filepath.Join(generations, entry.Name(), "library.img")
		info, statErr := os.Lstat(image)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			legacyBytes += stat.Blocks * 512
		} else {
			legacyBytes += info.Size()
		}
	}
	return map[string]int64{
		"combined_source_gamecube_payload_bytes": sourceBytes,
		"virtual_gamecube_disk_size":             manifest.VolumeSize,
		"generated_metadata_apparent_bytes":      metadataApparent,
		"generated_metadata_allocated_bytes":     metadataAllocated,
		"overlay_apparent_bytes":                 overlayApparent,
		"overlay_allocated_bytes":                overlayAllocated,
		"legacy_copied_generation_bytes":         legacyBytes,
		"mapped_title_count":                     int64(manifest.TitleCount),
		"mapped_disc_count":                      int64(manifest.DiscCount),
		"mapped_virtual_file_count":              int64(manifest.MappedFileCount),
		"mapped_extent_count":                    int64(manifest.MappedExtentCount),
		// Compatibility aliases retained for existing report consumers.
		"combined_source_payload_bytes":             sourceBytes,
		"new_generation_apparent_virtual_size":      manifest.VolumeSize,
		"new_generation_physically_allocated_bytes": metadataAllocated,
		"mapped_files":   int64(manifest.MappedFileCount),
		"mapped_extents": int64(manifest.MappedExtentCount),
		"overlay_bytes":  overlayAllocated,
	}, nil
}
