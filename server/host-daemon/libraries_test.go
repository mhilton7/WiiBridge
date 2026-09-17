package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wiibridge/server/host-daemon/gamecube"
	"wiibridge/server/host-daemon/store"
	"wiibridge/shared/sourcehealth"
	"wiibridge/tests/testutil"
)

func TestConfiguredSeparateLibraryPaths(t *testing.T) {
	t.Setenv("WIIBRIDGE_LIBRARY", "/library")
	t.Setenv("WIIBRIDGE_WII_LIBRARY", "")
	t.Setenv("WIIBRIDGE_GAMECUBE_LIBRARY", "")
	wii, gc, err := configuredLibraryRoots()
	if err != nil || wii != "/library" || gc != wii {
		t.Fatalf("legacy roots: %q %q %v", wii, gc, err)
	}
	t.Setenv("WIIBRIDGE_GAMECUBE_LIBRARY", "/games/gamecube/")
	wii, gc, err = configuredLibraryRoots()
	if err != nil || wii != "/library" || gc != "/games/gamecube" {
		t.Fatalf("mixed roots: %q %q %v", wii, gc, err)
	}
	t.Setenv("WIIBRIDGE_WII_LIBRARY", "/games/wii")
	wii, gc, err = configuredLibraryRoots()
	if err != nil || wii != "/games/wii" || gc != "/games/gamecube" {
		t.Fatalf("separate roots: %q %q %v", wii, gc, err)
	}
	t.Setenv("WIIBRIDGE_WII_LIBRARY", "relative")
	if _, _, err = configuredLibraryRoots(); err == nil {
		t.Fatal("relative root accepted")
	}
}

// Pipeline tests use synthetic sources on writable temporary directories.
// Production-handler tests below independently exercise the read-only guard.
func syntheticReadOnly(string) error { return nil }

func separateLibraryApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	a.root, a.gcRoot = t.TempDir(), t.TempDir()
	if err := testutil.SyntheticWBFS(filepath.Join(a.root, "wii.wbfs"), "SWII01", "Synthetic Wii", 2<<20); err != nil {
		t.Fatal(err)
	}
	if err := testutil.SyntheticGameCubeISO(filepath.Join(a.gcRoot, "gc.iso"), "SGCE01", "Synthetic GC", 0, 0, 2<<20); err != nil {
		t.Fatal(err)
	}
	var err error
	a.store, err = store.Open(filepath.Join(a.dataDir, "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.Close() })
	config := gamecube.DefaultLibraryConfig()
	config.SourceRoot = a.gcRoot
	a.gcLibrary, err = gamecube.NewLibraryManager(filepath.Join(a.dataDir, "gamecube/library"), config)
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"wii", "gamecube"} {
		preflight, preflightErr := sourcehealth.Preflight(a.libraryRoot(platform), nil)
		if preflightErr != nil {
			t.Fatal(preflightErr)
		}
		if platform == "wii" {
			a.source = preflight.Record
		} else {
			a.gcSource = preflight.Record
		}
		if err = a.scanLibraryGroup([]string{platform}, false, syntheticReadOnly); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func TestSeparateScansPreserveOtherPlatformWhenSourceMissing(t *testing.T) {
	a := separateLibraryApp(t)
	gcBefore, _ := a.store.Catalog("gamecube")
	if err := os.Rename(a.gcRoot, a.gcRoot+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(a.gcRoot+"-offline", a.gcRoot) })
	if err := a.scanLibraryGroup([]string{"gamecube"}, false, syntheticReadOnly); err == nil {
		t.Fatal("missing GameCube source was accepted")
	}
	if !a.ready || a.source.State != sourcehealth.StateAvailable || a.gcSource.State != sourcehealth.StateMountMissing {
		t.Fatalf("one library failure affected the other: wii=%#v gc=%#v", a.source, a.gcSource)
	}
	if err := a.scanLibraryGroup([]string{"wii"}, false, syntheticReadOnly); err != nil {
		t.Fatal(err)
	}
	gcAfter, _ := a.store.Catalog("gamecube")
	if len(gcAfter) != 1 || string(gcBefore[0].Payload) != string(gcAfter[0].Payload) || gcAfter[0].MissingObservations != 0 {
		t.Fatal("offline GameCube catalog changed during Wii scan")
	}
	response := httptest.NewRecorder()
	a.sourceStatusAPI(response, httptest.NewRequest(http.MethodGet, "/api/v1/sources", nil))
	var status struct {
		Sources map[string]sourcehealth.Record `json:"sources"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Sources["wii"].RootPath != a.root || status.Sources["gamecube"].RootPath != a.gcRoot {
		t.Fatal("source API lost separate paths")
	}
}

func TestMovedGameCubeGenerationDoesNotBlockRebuild(t *testing.T) {
	a := separateLibraryApp(t)
	old, err := a.gcLibrary.Build(context.Background(), a.gcScan.Games)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(a.gcRoot, "gc.iso"), filepath.Join(a.gcRoot, "moved.iso")); err != nil {
		t.Fatal(err)
	}
	if err = a.scanLibraryGroup([]string{"gamecube"}, false, syntheticReadOnly); err != nil {
		t.Fatal(err)
	}
	if a.gcSource.State != sourcehealth.StateAvailable || a.gcScan.Games[0].Availability != "playable" || !a.gcUpdate {
		t.Fatalf("fresh sources blocked by old generation: %#v %#v", a.gcSource, a.gcScan)
	}
	if _, err = a.gcLibrary.Active(); err == nil {
		t.Fatal("generation with moved source paths remained usable")
	}
	// A Wii outage must not block building from healthy GameCube sources.
	a.source.State, a.ready = sourcehealth.StateMountMissing, false
	response := httptest.NewRecorder()
	a.buildGameCubeLibrary(response, httptest.NewRequest(http.MethodPost, "/api/v1/gamecube/library/build", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("rebuild blocked: %d %s", response.Code, response.Body.String())
	}
	t.Cleanup(func() { a.gcLibrary.Cancel() })
	deadline := time.Now().Add(10 * time.Second)
	for a.gcLibrary.Progress().State == "Building" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	current, err := a.gcLibrary.Active()
	if err != nil || current.GenerationID == old.GenerationID || !strings.HasSuffix(current.Files[0].SourcePath, "moved.iso") {
		t.Fatalf("replacement generation failed: err=%v progress=%#v", err, a.gcLibrary.Progress())
	}
}

func TestAcceptReplacementMountRecoversPersistedIdentity(t *testing.T) {
	a := separateLibraryApp(t)
	a.gcSource.FilesystemID, a.gcSource.RootInode = "", 0
	a.gcSource.LastKnownMountInfo = "synthetic:previous:mount"
	a.gcSource.LastKnownDevice++
	if err := a.store.UpsertSource(a.gcSource); err != nil {
		t.Fatal(err)
	}
	trusted := a.gcSource
	for i := 0; i < 2; i++ {
		if err := a.scanLibraryGroup([]string{"gamecube"}, false, syntheticReadOnly); err == nil {
			t.Fatal("ordinary rescan accepted an unexpected mount")
		}
	}
	if err := a.scanLibraryGroup([]string{"gamecube"}, true, syntheticReadOnly); err != nil {
		t.Fatal(err)
	}
	persisted, err := a.store.SourceByRoot(a.gcRoot)
	if err != nil || persisted.State != sourcehealth.StateAvailable || persisted.LastKnownMountInfo == trusted.LastKnownMountInfo || persisted.LastSuccessfulItemCount != 1 {
		t.Fatalf("new identity not committed: %#v %v", persisted, err)
	}
	if _, err = sourcehealth.Preflight(a.gcRoot, &persisted); err != nil {
		t.Fatalf("saved identity fails after recovery: %v", err)
	}
	if !a.ready || a.source.State != sourcehealth.StateAvailable {
		t.Fatal("GameCube recovery affected Wii readiness")
	}
}

func TestReplacementWithoutValidGamesPreservesTrustedIdentityAndCatalog(t *testing.T) {
	a := separateLibraryApp(t)
	trusted := a.gcSource
	if err := os.Remove(filepath.Join(a.gcRoot, "gc.iso")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.gcRoot, "notes.txt"), []byte("not a game"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.scanLibraryGroup([]string{"gamecube"}, true, syntheticReadOnly); err == nil {
		t.Fatal("replacement without valid games was accepted")
	}
	persisted, _ := a.store.SourceByRoot(a.gcRoot)
	items, _ := a.store.Catalog("gamecube")
	if persisted.LastKnownMountInfo != trusted.LastKnownMountInfo || persisted.LastKnownDevice != trusted.LastKnownDevice || persisted.LastSuccessfulItemCount != 1 || len(items) != 1 || items[0].MissingObservations != 0 {
		t.Fatal("failed replacement changed trusted identity or catalog")
	}
}

func TestMountLostDuringScanDoesNotCommitReplacement(t *testing.T) {
	a := separateLibraryApp(t)
	trusted := a.gcSource
	calls := 0
	err := a.scanLibraryGroup([]string{"gamecube"}, true, func(string) error {
		calls++
		if calls == 2 {
			return errors.New("mount disappeared")
		}
		return nil
	})
	if err == nil {
		t.Fatal("mount disappearance was accepted")
	}
	persisted, _ := a.store.SourceByRoot(a.gcRoot)
	if !persisted.LastSuccessfulScan.Equal(trusted.LastSuccessfulScan) {
		t.Fatal("failed scan advanced successful scan")
	}
}

func TestRelocationRequiresAuthenticationConfirmationAndReadOnlySource(t *testing.T) {
	a := separateLibraryApp(t)
	token := "synthetic-library-administrator-token"
	a.tokenSum = sha256.Sum256([]byte(token))
	for _, test := range []struct {
		name, auth, csrf, confirm string
		want                      int
	}{
		{"unauthenticated", "", "", "relocate", http.StatusUnauthorized},
		{"missing csrf", token, "", "relocate", http.StatusForbidden},
		{"missing confirmation", token, "1", "", http.StatusBadRequest},
		{"writable replacement", token, "1", "relocate", http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{"platform": {"gamecube"}, "confirm": {test.confirm}}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/sources/relocate", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.auth != "" {
				request.Header.Set("Authorization", "Bearer "+test.auth)
			}
			request.Header.Set("X-WiiBridge-CSRF", test.csrf)
			response := httptest.NewRecorder()
			a.auth(a.acceptSourceLocation)(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if !a.ready || a.source.State != sourcehealth.StateAvailable {
		t.Fatal("failed GameCube request affected Wii")
	}
}

func TestDashboardShowsSeparateLibraryRecoveryControls(t *testing.T) {
	a := separateLibraryApp(t)
	response := httptest.NewRecorder()
	a.dashboard(response, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, required := range []string{a.root, a.gcRoot, "Rescan Wii library", "Rescan GameCube library", "Accept location and rescan", `name="confirm" value="relocate" required`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Errorf("dashboard missing %q", required)
		}
	}
}

func TestSharedRootPartialTraversalPreservesBothCatalogs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission traversal fault cannot be induced as root")
	}
	a := separateLibraryApp(t)
	if err := os.Rename(filepath.Join(a.gcRoot, "gc.iso"), filepath.Join(a.root, "gc.iso")); err != nil {
		t.Fatal(err)
	}
	a.gcRoot = a.root
	if err := a.scanLibraryGroup([]string{"wii", "gamecube"}, false, syntheticReadOnly); err != nil {
		t.Fatal(err)
	}
	before := a.source.LastSuccessfulScan
	blocked := filepath.Join(a.root, "unreadable")
	if err := os.Mkdir(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if err := a.scanLibraryGroup([]string{"wii", "gamecube"}, true, syntheticReadOnly); err == nil {
		t.Fatal("partial shared scan was accepted")
	}
	for _, platform := range []string{"wii", "gamecube"} {
		items, err := a.store.Catalog(platform)
		if err != nil || len(items) != 1 || items[0].MissingObservations != 0 {
			t.Fatalf("%s catalog changed on partial scan", platform)
		}
	}
	if !a.source.LastSuccessfulScan.Equal(before) || !a.gcSource.LastSuccessfulScan.Equal(before) {
		t.Fatal("partial scan changed successful scan history")
	}
}

func TestGameCubeRuntimeFailureDoesNotChangeWiiState(t *testing.T) {
	a := separateLibraryApp(t)
	a.sourceFailures = make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.runSourceFailureReconciler(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	a.queueGameCubeSourceFailure("SOURCE-READ-FAILED")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.RLock()
		failed := a.gcSource.FailureCode == "SOURCE-READ-FAILED"
		wiiReady := a.ready && a.source.State == sourcehealth.StateAvailable
		a.mu.RUnlock()
		if failed {
			if !wiiReady {
				t.Fatal("GameCube failure changed Wii readiness")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("GameCube source failure was not reconciled")
}
