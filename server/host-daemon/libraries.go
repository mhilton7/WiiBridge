package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"wiibridge/server/host-daemon/gamecube"
	"wiibridge/server/host-daemon/scanner"
	"wiibridge/server/host-daemon/store"
	"wiibridge/server/host-daemon/vdisk"
	"wiibridge/shared/model"
	"wiibridge/shared/sourcehealth"
)

func configuredLibraryRoots() (string, string, error) {
	shared := env("WIIBRIDGE_LIBRARY", "/library")
	wii := env("WIIBRIDGE_WII_LIBRARY", shared)
	gc := env("WIIBRIDGE_GAMECUBE_LIBRARY", shared)
	if !filepath.IsAbs(wii) || !filepath.IsAbs(gc) {
		return "", "", errors.New("Wii and GameCube library paths must be absolute")
	}
	return filepath.Clean(wii), filepath.Clean(gc), nil
}

func (a *app) libraryRoot(platform string) string {
	if platform == "gamecube" && a.gcRoot != "" {
		return a.gcRoot
	}
	return a.root
}

// Caller holds a.mu. The fallback supports the shared-root startup state until
// GameCube's first scan has established its own catalog availability.
func (a *app) librarySourceLocked(platform string) sourcehealth.Record {
	if platform == "gamecube" {
		if a.gcSource.State != "" {
			return a.gcSource
		}
		if a.libraryRoot(platform) != a.root {
			return sourcehealth.Record{RootPath: a.libraryRoot(platform), State: sourcehealth.StateUnknown}
		}
	}
	return a.source
}

func previousPlatformSource(database *store.Store, root, platform string) *sourcehealth.Record {
	if platform == "wii" {
		return previousSource(database, root)
	}
	absolute, _ := filepath.Abs(root)
	if record, err := database.SourceByRoot(absolute); err == nil {
		return &record
	}
	items, err := database.Catalog(platform)
	if err != nil || len(items) == 0 {
		return nil
	}
	preflight, _ := sourcehealth.Preflight(root, nil)
	record := preflight.Record
	record.LastSuccessfulItemCount = len(items)
	return &record
}

func (a *app) acceptSourceLocation(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("confirm") != "relocate" {
		http.Error(w, "Confirm that the configured folder is the intended library location.", http.StatusBadRequest)
		return
	}
	if platform := r.FormValue("platform"); platform != "wii" && platform != "gamecube" {
		http.Error(w, "Select Wii or GameCube for location recovery.", http.StatusBadRequest)
		return
	}
	a.rescanLibraries(w, r, true)
}

func (a *app) rescanLibraries(w http.ResponseWriter, r *http.Request, acceptReplacement bool) {
	platform := r.FormValue("platform")
	if platform != "" && platform != "all" && platform != "wii" && platform != "gamecube" {
		http.Error(w, "Unknown library platform.", http.StatusBadRequest)
		return
	}
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	groups := [][]string{{"wii", "gamecube"}}
	if a.root != a.libraryRoot("gamecube") {
		groups = nil
		for _, name := range []string{"wii", "gamecube"} {
			if platform == "" || platform == "all" || platform == name {
				groups = append(groups, []string{name})
			}
		}
	}
	results := make(map[string]any)
	failed := false
	for _, group := range groups {
		err := a.scanLibraryGroup(group, acceptReplacement, assertReadOnly)
		a.mu.RLock()
		record := a.librarySourceLocked(group[0])
		a.mu.RUnlock()
		status, message := "complete", "Library rescan completed."
		if err != nil {
			failed, status, message = true, "preserved", err.Error()
		}
		for _, name := range group {
			results[name] = map[string]any{"status": status, "source": record, "message": message}
		}
	}
	if failed {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"status": "incomplete", "libraries": results,
			"message": "See each library result. Failed libraries retained their prior catalogs.",
		})
		return
	}
	respondAction(w, r, http.StatusOK, map[string]any{"status": "complete", "libraries": results},
		"Library scan completed. Rebuild the GameCube library if an update is available.", "all")
}

// scanLibraryGroup handles both catalogs atomically for a shared root, or one
// catalog for a dedicated root. Caller serializes scans with scanMu.
func (a *app) scanLibraryGroup(platforms []string, acceptReplacement bool,
	verifyReadOnly func(string) error,
) error {
	root := a.libraryRoot(platforms[0])
	a.mu.RLock()
	previous := a.librarySourceLocked(platforms[0])
	a.mu.RUnlock()
	if previous.SourceID == "" {
		if saved := previousPlatformSource(a.store, root, platforms[0]); saved != nil {
			previous = *saved
		}
	}
	if safetyErr := verifyReadOnly(root); safetyErr != nil && !isUnavailableLibraryError(safetyErr) {
		record := sourcehealth.RuntimeFailure(previous, "SOURCE-READONLY-GUARANTEE-FAILED")
		a.recordLibraryFailure(platforms, record)
		return errors.New("Source is not provably read-only; prior catalogs were preserved.")
	}
	preflight, err := sourcehealth.Preflight(root, &previous)
	if acceptReplacement {
		preflight, err = sourcehealth.PreflightReplacement(root, previous)
	}
	if err != nil {
		a.recordLibraryFailure(platforms, preflight.Record)
		return errors.New(preflight.Record.FailureMessage)
	}
	// Retain the trusted identity on every failure after candidate preflight.
	fail := func(cause error) error {
		record := sourcehealth.Partial(previous, cause)
		a.recordLibraryFailure(platforms, record)
		return cause
	}
	var wii scanner.Result
	var gc gamecube.Result
	catalogs := make(map[string][]store.CatalogItem)
	count := 0
	for _, platform := range platforms {
		switch platform {
		case "wii":
			wii, err = scanner.Scan(root)
			if err == nil {
				catalogs[platform], err = wiiCatalogItems(wii.Games)
			}
			count += len(wii.Games)
		case "gamecube":
			gc, err = gamecube.Scan(root)
			if err == nil {
				catalogs[platform], err = gameCubeCatalogItems(gc.Games)
			}
			count += len(gc.Games)
		}
		if err != nil {
			return fail(errors.New("Source scan was partial; prior catalogs were preserved."))
		}
	}
	if acceptReplacement && count == 0 && previous.LastSuccessfulItemCount > 0 {
		return fail(errors.New("No valid games were found in the replacement location; the prior catalog was preserved."))
	}
	// A mount can disappear or be replaced while discovery is running.
	if err = verifyReadOnly(root); err != nil {
		return fail(errors.New("The read-only source became unavailable during the scan."))
	}
	if _, err = sourcehealth.Preflight(root, &preflight.Record); err != nil {
		return fail(errors.New("The source mount changed during the scan; retry after restoring it."))
	}
	record := sourcehealth.Successful(preflight.Record, count)
	var disk *vdisk.Disk
	err = a.store.CommitSourceScan(record, catalogs, func(reconciled map[string][]store.CatalogItem) (*model.Snapshot, error) {
		var snapshot *model.Snapshot
		if items, ok := reconciled["wii"]; ok {
			wii.Games, err = decodeWiiItems(items, sourcehealth.StateAvailable)
			if err != nil {
				return nil, err
			}
			disk, err = vdisk.Build("all", wii.Games, version)
			if err != nil {
				return nil, err
			}
			value := disk.Snapshot()
			if err = a.writeSnapshotFile(value); err != nil {
				return nil, err
			}
			snapshot = &value
		}
		if items, ok := reconciled["gamecube"]; ok {
			gc.Games, err = decodeGameCubeItems(items, sourcehealth.StateAvailable)
			if err != nil {
				return nil, err
			}
		}
		return snapshot, nil
	})
	if err != nil {
		return fail(errors.New("Catalog validation or persistence failed; prior catalogs were preserved."))
	}
	if disk != nil {
		disk.SetObserver(a.metricsRegistry, a.queueSourceFailure)
	}
	gcUpdate := false
	if _, scanned := catalogs["gamecube"]; scanned {
		if _, managedErr := a.gcLibrary.ManagedActive(); managedErr == nil {
			gcUpdate = true
			// The old generation may refer to old paths. Block that generation,
			// but leave newly discovered sources playable so rebuilding works.
			_ = a.gcLibrary.RecheckActive()
		}
	}
	a.mu.Lock()
	if disk != nil {
		a.scan, a.disk, a.source, a.ready = wii, disk, record, true
	}
	if _, scanned := catalogs["gamecube"]; scanned {
		a.gcScan, a.gcSource, a.gcUpdate = gc, record, gcUpdate
		a.gcStartupPhase, a.gcStartupError = "Scan complete", ""
	}
	a.mu.Unlock()
	return nil
}

func (a *app) recordLibraryFailure(platforms []string, record sourcehealth.Record) {
	if a.store != nil {
		_ = a.store.UpsertSource(record)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, platform := range platforms {
		if platform == "wii" {
			a.source, a.ready = record, false
			for index := range a.scan.Games {
				a.scan.Games[index].Availability = string(sourcehealth.DerivedAvailability(record.State, sourcehealth.AvailabilityPlayable))
			}
		} else {
			a.gcSource = record
			for index := range a.gcScan.Games {
				a.gcScan.Games[index].Availability = string(sourcehealth.DerivedAvailability(record.State, sourcehealth.AvailabilityPlayable))
			}
		}
	}
}

func (a *app) queueGameCubeSourceFailure(code string) {
	a.queueSourceFailure("gamecube:" + code)
}

func sourceFailurePlatform(value string) (string, string) {
	if code, ok := strings.CutPrefix(value, "gamecube:"); ok {
		return "gamecube", normalizedSourceFailureCode(code)
	}
	return "wii", normalizedSourceFailureCode(value)
}
