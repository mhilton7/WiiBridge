package gamecube

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"wiibridge/tests/testutil"
)

func libraryGames(t testing.TB, root string) []Game {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		name, id, title string
		disc, revision  byte
	}{
		{"alpha.iso", "GALP01", "Alpha/Game", 0, 0},
		{"beta-1.gcm", "GBET01", "Beta Game", 0, 1},
		{"beta-2.gcm", "GBET01", "Beta Game", 1, 1},
	}
	for _, fixture := range fixtures {
		if err := testutil.SyntheticGameCubeISO(filepath.Join(root, fixture.name),
			fixture.id, fixture.title, fixture.disc, fixture.revision, 2<<20); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Games) != 2 {
		t.Fatalf("games=%d rejections=%#v", len(result.Games), result.Rejected)
	}
	return result.Games
}

func libraryManager(t testing.TB, managed, sources string) *LibraryManager {
	t.Helper()
	config := DefaultLibraryConfig()
	config.SourceRoot = sources
	manager, err := NewLibraryManager(managed, config)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestChangingConfiguredLibraryRootRequiresNewGeneration(t *testing.T) {
	oldRoot, newRoot, managed := t.TempDir(), t.TempDir(), t.TempDir()
	oldManager := libraryManager(t, managed, oldRoot)
	old, err := oldManager.Build(context.Background(), libraryGames(t, oldRoot))
	if err != nil {
		t.Fatal(err)
	}
	manager := libraryManager(t, managed, newRoot)
	if _, ready := manager.ValidatedSummary(); ready {
		t.Fatal("old folder remained active after path change")
	}
	if _, err = manager.Active(); !errors.Is(err, ErrGameCubeSourceChanged) {
		t.Fatalf("old generation path accepted: %v", err)
	}
	if retained, retainedErr := manager.ManagedActive(); retainedErr != nil || retained.GenerationID != old.GenerationID {
		t.Fatal("old generation was not retained")
	}
	current, err := manager.Build(context.Background(), libraryGames(t, newRoot))
	if err != nil || current.LibraryRoot != newRoot {
		t.Fatalf("new folder could not be built: %v", err)
	}
	if _, err = manager.Active(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteLibraryIsNoCopyAndReadsEveryDisc(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	before := make(map[string]string)
	var sourceBytes int64
	for _, game := range games {
		for _, disc := range game.Discs {
			sum, err := hashFile(disc.SourcePath)
			if err != nil {
				t.Fatal(err)
			}
			before[disc.SourcePath] = sum
			sourceBytes += disc.PhysicalSize
		}
	}
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), games)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != 2 || manifest.TitleCount != 2 || manifest.DiscCount != 3 ||
		!manifest.ReadOnly || manifest.MappedFileCount != 3 {
		t.Fatalf("manifest=%#v", manifest)
	}
	paths := make(map[string]bool)
	for _, file := range manifest.Files {
		paths[file.VirtualPath] = true
	}
	if !paths["/games/Alpha_Game [GALP01]/game.iso"] ||
		!paths["/games/Beta Game [GBET01] [Rev 1]/game.iso"] ||
		!paths["/games/Beta Game [GBET01] [Rev 1]/disc2.iso"] {
		t.Fatalf("unexpected Nintendont namespace: %#v", paths)
	}
	generation := filepath.Dir(manifest.LayoutPath)
	if _, err = os.Stat(filepath.Join(generation, "library.img")); !os.IsNotExist(err) {
		t.Fatal("no-copy generation contains library.img")
	}
	var allocated int64
	err = filepath.Walk(generation, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			allocated += stat.Blocks * 512
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if allocated >= sourceBytes/2 {
		t.Fatalf("metadata storage scales like payload: allocated=%d source=%d", allocated, sourceBytes)
	}
	report, err := StorageReport(manifest)
	if err != nil {
		t.Fatal(err)
	}
	reportData, _ := json.Marshal(report)
	t.Logf("no-copy storage report: %s", reportData)
	if report["mapped_title_count"] != 2 || report["mapped_disc_count"] != 3 ||
		report["mapped_virtual_file_count"] != 3 || report["mapped_extent_count"] != 3 ||
		report["overlay_apparent_bytes"] != 0 || report["overlay_allocated_bytes"] != 0 ||
		report["generated_metadata_allocated_bytes"] >= sourceBytes/2 {
		t.Fatalf("incomplete or payload-sized storage report: %#v", report)
	}
	backend, err := OpenLibraryBackend(manager.Root(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for _, file := range manifest.Files {
		expected, err := os.ReadFile(file.SourcePath)
		if err != nil {
			t.Fatal(err)
		}
		extent := findManifestExtent(t, manifest, file.VirtualPath)
		actual := make([]byte, len(expected))
		if _, err = backend.ReadAt(actual, extent); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatalf("virtual payload differs for %s", file.VirtualPath)
		}
	}
	for path, expected := range before {
		actual, hashErr := hashFile(path)
		if hashErr != nil || actual != expected {
			t.Fatalf("source changed: path=%s hash=%s err=%v", path, actual, hashErr)
		}
	}
}

func TestRetainedGenerationsDoNotMultiplyPayloadStorage(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	var sourceBytes int64
	for _, game := range games {
		for _, disc := range game.Discs {
			sourceBytes += disc.PhysicalSize
		}
	}
	for build := 0; build < 2; build++ {
		if _, err := manager.Build(context.Background(), games); err != nil {
			t.Fatal(err)
		}
	}
	var allocated int64
	err := filepath.Walk(filepath.Join(manager.Root(), "generations"),
		func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.Name() == "library.img" {
				t.Fatalf("retained schema-2 generation contains %s", path)
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				allocated += stat.Blocks * 512
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if allocated >= sourceBytes {
		t.Fatalf("retained metadata allocated=%d source payload=%d", allocated, sourceBytes)
	}
}

func TestFastStartupValidationDoesNotHashPayload(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), games[:1])
	if err != nil {
		t.Fatal(err)
	}
	source := manifest.Files[0].SourcePath
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	original := make([]byte, 1)
	readSource, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readSource.ReadAt(original, 0x1000); err != nil {
		readSource.Close()
		t.Fatal(err)
	}
	readSource.Close()
	file, err := os.OpenFile(source, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteAt([]byte{original[0] ^ 0xff}, 0x1000); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(source, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err = ValidateLibraryManifestFast(manager.Root(), manifest); err != nil {
		t.Fatalf("fast validation read payload content: %v", err)
	}
	if err = ValidateLibraryManifest(manager.Root(), manifest); err == nil ||
		!strings.Contains(err.Error(), "fingerprint changed") {
		t.Fatalf("deep validation did not detect content change: %v", err)
	}
}

func TestManagerDefersDeepValidationAndStartupCleanup(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	managed := filepath.Join(root, "managed")
	manager := libraryManager(t, managed, sourceRoot)
	manifest, err := manager.Build(context.Background(), games[:1])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(validationReceiptPath(manifest)); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(managed, "generations", ".building-preserved", "marker")
	if err = os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(stale, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	restarted := libraryManager(t, managed, sourceRoot)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("manager construction performed deep startup work: %s", elapsed)
	}
	if restarted.Progress().Validation != "pending" ||
		restarted.Progress().State != "Validating" {
		t.Fatalf("unexpected deferred state: %#v", restarted.Progress())
	}
	if _, ready := restarted.ValidatedSummary(); ready {
		t.Fatal("generation without deep-validation receipt was reported ready")
	}
	if _, err = os.Stat(stale); err != nil {
		t.Fatalf("constructor deleted stale generation data: %v", err)
	}
	if _, err = restarted.Active(); err == nil {
		t.Fatal("generation without deep-validation receipt became active")
	}
	if err = restarted.StartActiveValidation(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if restarted.Progress().State == "Ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if restarted.Progress().State != "Ready" {
		t.Fatalf("background validation did not finish: %#v", restarted.Progress())
	}
	if active, activeErr := restarted.Active(); activeErr != nil ||
		active.GenerationID != manifest.GenerationID {
		t.Fatalf("validated generation not restored: %#v err=%v", active, activeErr)
	}
	if summary, ready := restarted.ValidatedSummary(); !ready ||
		summary.GenerationID != manifest.GenerationID {
		t.Fatalf("validated summary not restored: %#v ready=%v", summary, ready)
	}
}

func BenchmarkFastStartupValidation(b *testing.B) {
	root := b.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(b, sourceRoot)
	manager := libraryManager(b, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), games)
	if err != nil {
		b.Fatal(err)
	}
	var payloadBytes int64
	for _, file := range manifest.Files {
		payloadBytes += file.LogicalSize
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err = ValidateLibraryManifestFast(manager.Root(), manifest); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(payloadBytes), "payload_bytes")
	b.ReportMetric(float64(len(manifest.Files)), "source_files")
}

func TestCompleteLibraryMapsCISOWithoutConversion(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(sourceRoot, "source.tmp")
	if err := testutil.SyntheticGameCubeISO(iso, "GCSE01", "CISO Game", 0, 0, 2<<20); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(iso)
	if err != nil {
		t.Fatal(err)
	}
	ciso := make([]byte, int(cisoHeader)+len(payload))
	copy(ciso[:4], "CISO")
	binary.LittleEndian.PutUint32(ciso[4:8], uint32(cisoBlock))
	ciso[8] = 1
	copy(ciso[cisoHeader:], payload)
	source := filepath.Join(sourceRoot, "game.cso")
	if err = os.WriteFile(source, ciso, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(iso); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(sourceRoot)
	if err != nil || len(result.Games) != 1 {
		t.Fatalf("scan=%#v err=%v", result, err)
	}
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), result.Games)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 ||
		!strings.HasSuffix(manifest.Files[0].VirtualPath, "/game.ciso") ||
		manifest.Files[0].SourcePath != source {
		t.Fatalf("CISO mapping=%#v", manifest.Files)
	}
}

func TestCompleteLibraryPathContainsNoPayloadCopyCalls(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(current), "library.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"copyFileToFilesystem", "copyTreeToFilesystem"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("complete-library path still references %s", forbidden)
		}
	}
}

func findManifestExtent(t *testing.T, manifest LibraryManifest, virtualPath string) int64 {
	t.Helper()
	data, err := os.ReadFile(manifest.LayoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		SourceExtents []struct {
			VirtualOffset int64  `json:"virtual_offset"`
			SourcePath    string `json:"source_path"`
		} `json:"source_extents"`
	}
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var source string
	for _, file := range manifest.Files {
		if file.VirtualPath == virtualPath {
			source = file.SourcePath
			break
		}
	}
	for _, extent := range document.SourceExtents {
		if extent.SourcePath == source {
			return extent.VirtualOffset
		}
	}
	t.Fatalf("extent not found for %s", virtualPath)
	return 0
}

func TestLibraryReadCrossesPayloadPaddingAndReturnsZeroes(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)[:1]
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), games)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := OpenLibraryBackend(manager.Root(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	file := manifest.Files[0]
	start := findManifestExtent(t, manifest, file.VirtualPath)
	buffer := make([]byte, 1024)
	if _, err = backend.ReadAt(buffer, start+file.LogicalSize-256); err != nil {
		t.Fatal(err)
	}
	expected, _ := os.ReadFile(file.SourcePath)
	if !bytes.Equal(buffer[:256], expected[len(expected)-256:]) {
		t.Fatal("cross-boundary source bytes differ")
	}
	if !bytes.Equal(buffer[256:], make([]byte, len(buffer)-256)) {
		t.Fatal("cluster padding was not zero-filled")
	}
}

func TestPhysicalLibraryRejectsWrites(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), games[:1])
	if err != nil {
		t.Fatal(err)
	}
	backend, err := OpenLibraryBackend(manager.Root(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if _, err = backend.WriteAt([]byte{1}, 0); !errorsIsPermission(err) {
		t.Fatalf("physical backend write error=%v", err)
	}
}

func TestEmulatedIndividualLibraryWritesOnlyTrustedSaveExtent(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)[:1]
	sourceHash, err := hashFile(games[0].Discs[0].SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultLibraryConfig()
	config.SourceRoot = sourceRoot
	config.SavesRoot = filepath.Join(root, "gamecube", "saves")
	config.Mode = MemoryCardEmulatedIndividual
	config.CardSize = 512 << 10
	config.Application = "test"
	manager, err := NewLibraryManager(filepath.Join(root, "gamecube", "library"), config)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.Build(context.Background(), games)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ReadOnly || manifest.SaveOverlayVersion != SaveOverlayFormatVersion ||
		len(manifest.SaveObjects) != 1 || manifest.SaveExtentCount != 1 {
		t.Fatalf("manifest=%#v", manifest)
	}
	layoutData, err := os.ReadFile(manifest.LayoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var layout struct {
		SaveExtents []struct {
			VirtualOffset int64 `json:"virtual_offset"`
		} `json:"save_extents"`
		SourceExtents []struct {
			VirtualOffset int64 `json:"virtual_offset"`
		} `json:"source_extents"`
	}
	if err = json.Unmarshal(layoutData, &layout); err != nil {
		t.Fatal(err)
	}
	backend, err := OpenLibraryBackend(manager.Root(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	saveOffset := layout.SaveExtents[0].VirtualOffset
	payloadOffset := layout.SourceExtents[0].VirtualOffset
	expected := bytes.Repeat([]byte{0x5c}, 1024)
	if count, writeErr := backend.WriteAt(expected, saveOffset+512); writeErr != nil ||
		count != len(expected) {
		t.Fatalf("save write=%d err=%v", count, writeErr)
	}
	if _, writeErr := backend.WriteAt([]byte{1}, payloadOffset); writeErr == nil ||
		!strings.Contains(writeErr.Error(), "SAVE-WRITE-OUTSIDE-EXTENT") {
		t.Fatalf("payload write error=%v", writeErr)
	}
	if _, writeErr := backend.WriteAt(make([]byte, 1024),
		saveOffset+manifest.SaveObjects[0].CardSize-512); writeErr == nil ||
		!strings.Contains(writeErr.Error(), "SAVE-WRITE-CROSSES-BOUNDARY") {
		t.Fatalf("boundary write error=%v", writeErr)
	}
	if err = backend.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = backend.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLibraryBackend(manager.Root(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual := make([]byte, len(expected))
	if _, err = reopened.ReadAt(actual, saveOffset+512); err != nil ||
		!bytes.Equal(actual, expected) {
		t.Fatalf("save persistence mismatch: %v", err)
	}
	after, err := hashFile(games[0].Discs[0].SourcePath)
	if err != nil || after != sourceHash {
		t.Fatalf("game source changed: %s %v", after, err)
	}
}

func errorsIsPermission(err error) bool {
	return err == os.ErrPermission
}

func TestSourceChangesInvalidateGeneration(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(string) error
	}{
		{"size", func(name string) error {
			file, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = file.Write([]byte{0})
			return err
		}},
		{"fingerprint", func(name string) error {
			file, err := os.OpenFile(name, os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = file.WriteAt([]byte{0xff}, 0x1000)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			sourceRoot := filepath.Join(root, "sources")
			games := libraryGames(t, sourceRoot)
			manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
			manifest, err := manager.Build(context.Background(), games[:1])
			if err != nil {
				t.Fatal(err)
			}
			if err = test.change(manifest.Files[0].SourcePath); err != nil {
				t.Fatal(err)
			}
			if _, err = OpenLibraryBackend(manager.Root(), manifest); err == nil {
				t.Fatal("changed source generation remained valid")
			}
		})
	}
}

func TestOfflineSourceRetainsGenerationAndValidationReceipt(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), games[:1])
	if err != nil {
		t.Fatal(err)
	}
	receipt := validationReceiptPath(manifest)
	if err = os.Remove(manifest.Files[0].SourcePath); err != nil {
		t.Fatal(err)
	}
	if err = manager.RecheckActive(); err == nil ||
		!errors.Is(err, ErrGameCubeSourceUnavailable) {
		t.Fatalf("offline recheck=%v", err)
	}
	if _, err = os.Stat(receipt); err != nil {
		t.Fatalf("offline source removed validation receipt: %v", err)
	}
	managed, err := manager.ManagedActive()
	if err != nil || managed.GenerationID != manifest.GenerationID {
		t.Fatalf("generation was not retained: %#v %v", managed, err)
	}
}

func TestChangedSourceRetainsGenerationButRevokesValidationReceipt(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), games[:1])
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(manifest.Files[0].SourcePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte{0}); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = manager.RecheckActive(); err == nil ||
		!errors.Is(err, ErrGameCubeSourceChanged) {
		t.Fatalf("changed recheck=%v", err)
	}
	if _, err = os.Stat(validationReceiptPath(manifest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed source retained validation receipt: %v", err)
	}
	managed, err := manager.ManagedActive()
	if err != nil || managed.GenerationID != manifest.GenerationID {
		t.Fatalf("changed source destroyed generation: %#v %v", managed, err)
	}
}

func TestSourceEscapeAndSymlinkRejected(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	outside := filepath.Join(root, "outside.iso")
	if err := os.Rename(games[0].Discs[0].SourcePath, outside); err != nil {
		t.Fatal(err)
	}
	games[0].Discs[0].SourcePath = outside
	if _, err := manager.Build(context.Background(), games[:1]); err == nil {
		t.Fatal("source path escape accepted")
	}
	link := filepath.Join(sourceRoot, "link.iso")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	games[0].Discs[0].SourcePath = link
	if _, err := manager.Build(context.Background(), games[:1]); err == nil {
		t.Fatal("source symlink accepted")
	}
}

func TestExtractedFSTFilesMapToOriginalSources(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	fstRoot := filepath.Join(sourceRoot, "Extracted [GFST01]")
	if err := writeSyntheticFST(fstRoot, "GFST01"); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(sourceRoot)
	if err != nil || len(result.Games) != 1 {
		t.Fatalf("scan=%#v err=%v", result, err)
	}
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), result.Games)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"/sys/boot.bin":      filepath.Join(fstRoot, "sys", "boot.bin"),
		"/sys/main.dol":      filepath.Join(fstRoot, "sys", "main.dol"),
		"/files/fixture.bin": filepath.Join(fstRoot, "files", "fixture.bin"),
	}
	for suffix, source := range expected {
		found := false
		for _, file := range manifest.Files {
			if strings.HasSuffix(file.VirtualPath, suffix) {
				found = true
				if file.SourcePath != source || file.Format != "fst" {
					t.Fatalf("FST mapping=%#v want source=%s", file, source)
				}
			}
		}
		if !found {
			t.Fatalf("missing mapped FST file %s", suffix)
		}
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(manifest.LayoutPath), "library.img")); !os.IsNotExist(err) {
		t.Fatal("FST build created payload image")
	}
	if err = os.WriteFile(filepath.Join(fstRoot, "files", "added.bin"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = ValidateLibraryManifest(manager.Root(), manifest); err == nil ||
		!strings.Contains(err.Error(), "FST tree changed") {
		t.Fatalf("changed FST tree validation error=%v", err)
	}
}

func TestInvalidAndCanceledGenerationDoNotReplaceActive(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	first, err := manager.Build(context.Background(), games[:1])
	if err != nil {
		t.Fatal(err)
	}
	activeBefore, _ := os.ReadFile(filepath.Join(manager.Root(), "active.json"))
	bad := games[1]
	bad.Discs[0].SourcePath = filepath.Join(root, "missing.iso")
	if _, err = manager.Build(context.Background(), []Game{bad}); err == nil {
		t.Fatal("invalid rebuild succeeded")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = manager.Build(canceled, games); err == nil {
		t.Fatal("canceled rebuild succeeded")
	}
	activeAfter, _ := os.ReadFile(filepath.Join(manager.Root(), "active.json"))
	if !bytes.Equal(activeBefore, activeAfter) {
		t.Fatal("failed build replaced active generation")
	}
	active, err := manager.Active()
	if err != nil || active.GenerationID != first.GenerationID {
		t.Fatalf("active generation lost: %#v err=%v", active, err)
	}
}

func TestIncompleteOrInconsistentDiscSetRejected(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	incomplete := games[1]
	incomplete.Discs = incomplete.Discs[:1]
	if _, err := manager.Build(context.Background(), []Game{incomplete}); err == nil ||
		!strings.Contains(err.Error(), "incomplete disc set") {
		t.Fatalf("incomplete two-disc set error=%v", err)
	}
	inconsistent := games[0]
	inconsistent.Discs[0].Number = 1
	if _, err := manager.Build(context.Background(), []Game{inconsistent}); err == nil ||
		!strings.Contains(err.Error(), "inconsistent disc metadata") {
		t.Fatalf("inconsistent disc metadata error=%v", err)
	}
}

func TestLegacySchemaOneDetectedButNotDeleted(t *testing.T) {
	root := t.TempDir()
	generation := filepath.Join(root, "managed", "generations", "legacy-safe")
	if err := os.MkdirAll(generation, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "library.img"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := DefaultLibraryConfig()
	config.SourceRoot = root
	manager, err := NewLibraryManager(filepath.Join(root, "managed"), config)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.LegacyGenerations(); len(got) != 1 || got[0] != "legacy-safe" {
		t.Fatalf("legacy generations=%v", got)
	}
	if _, err = os.Stat(filepath.Join(generation, "library.img")); err != nil {
		t.Fatal("legacy image was automatically deleted")
	}
}

func TestLibrarySizingLimitsAndExplicitEmulatedRejection(t *testing.T) {
	config := DefaultLibraryConfig()
	size, err := CalculateLibrarySize(4<<30, config)
	if err != nil {
		t.Fatal(err)
	}
	if size < 5<<30 || size%libraryAlignment != 0 {
		t.Fatalf("unexpected volume size %d", size)
	}
	config.MaxVolumeGiB = 8
	if _, err = CalculateLibrarySize(20<<30, config); err == nil {
		t.Fatal("configured maximum was ignored")
	}
	if _, err = CalculateLibrarySize(math.MaxInt64, DefaultLibraryConfig()); err == nil {
		t.Fatal("integer overflow was accepted")
	}
	config = DefaultLibraryConfig()
	config.Mode = MemoryCardEmulated
	if _, err = NewLibraryManager(filepath.Join(t.TempDir(), "managed"), config); err == nil ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("emulated mode did not fail clearly: %v", err)
	}
}

func TestActivePointerRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	config := DefaultLibraryConfig()
	config.SourceRoot = root
	manager, err := NewLibraryManager(filepath.Join(root, "managed"), config)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"schema": LibrarySchema, "generation_id": "../escape"})
	if err = os.WriteFile(filepath.Join(manager.Root(), "active.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Active(); err == nil {
		t.Fatal("active generation traversal accepted")
	}
}

func TestUnallocatedVirtualSectorsAreZero(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "sources")
	games := libraryGames(t, sourceRoot)
	manager := libraryManager(t, filepath.Join(root, "managed"), sourceRoot)
	manifest, err := manager.Build(context.Background(), games[:1])
	if err != nil {
		t.Fatal(err)
	}
	backend, err := OpenLibraryBackend(manager.Root(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	buffer := make([]byte, 4096)
	if _, err = backend.ReadAt(buffer, backend.Size()-int64(len(buffer))); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer, make([]byte, len(buffer))) {
		t.Fatal("unallocated virtual range is not zero")
	}
}
