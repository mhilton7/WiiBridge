package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wiibridge/server/host-daemon/bridgecontrol"
	"wiibridge/server/host-daemon/gamecube"
	"wiibridge/server/host-daemon/scanner"
	"wiibridge/server/host-daemon/store"
	"wiibridge/shared/compat"
	"wiibridge/shared/contract"
	"wiibridge/shared/model"
	"wiibridge/shared/perf"
	"wiibridge/shared/sourcehealth"
)

func previousSource(database *store.Store, root string) *sourcehealth.Record {
	absolute, _ := filepath.Abs(root)
	record, err := database.SourceByRoot(absolute)
	if err == nil {
		return &record
	}
	// Schema-1 databases have an authoritative active Wii snapshot but no
	// source-root row. Preserve its item count as the preflight baseline so an
	// empty failed mount on the first upgraded boot cannot become a valid empty
	// reconciliation.
	snapshot, snapshotErr := database.Active()
	if snapshotErr != nil || len(snapshot.Games) == 0 {
		return nil
	}
	preflight, _ := sourcehealth.Preflight(absolute, nil)
	record = preflight.Record
	record.LastSuccessfulItemCount = len(snapshot.Games)
	record.LastSuccessfulScan = snapshot.Created
	return &record
}

func scanWiiCatalog(database *store.Store, root string) (
	scanner.Result, sourcehealth.Record, error,
) {
	preflight, preflightErr := sourcehealth.Preflight(root, previousSource(database, root))
	if preflightErr != nil {
		_ = database.UpsertSource(preflight.Record)
		_ = database.RecordSourceEvent(
			preflight.Record.SourceID, preflight.Record.FailureCode,
			preflight.Record.FailureMessage)
		cached, cacheErr := loadWiiCatalog(database, preflight.Record)
		if cacheErr != nil {
			return scanner.Result{}, preflight.Record, preflightErr
		}
		return cached, preflight.Record, preflightErr
	}
	result, err := scanner.Scan(root)
	if err != nil {
		record := sourcehealth.Partial(preflight.Record, err)
		_ = database.UpsertSource(record)
		_ = database.RecordSourceEvent(record.SourceID, record.FailureCode, record.FailureMessage)
		cached, cacheErr := loadWiiCatalog(database, record)
		if cacheErr != nil {
			return scanner.Result{}, record, err
		}
		return cached, record, err
	}
	successfulCount := len(result.Games)
	result, err = reconcileWiiResult(database, result)
	if err != nil {
		return scanner.Result{}, preflight.Record, err
	}
	record := sourcehealth.Successful(preflight.Record, successfulCount)
	if err = database.UpsertSource(record); err != nil {
		return scanner.Result{}, record, err
	}
	return result, record, nil
}

func reconcileWiiResult(database *store.Store, result scanner.Result) (scanner.Result, error) {
	items, err := wiiCatalogItems(result.Games)
	if err != nil {
		return scanner.Result{}, err
	}
	reconciled, err := database.ReconcileCatalog("wii", items, 2)
	if err != nil {
		return scanner.Result{}, err
	}
	result.Games, err = decodeWiiItems(reconciled, sourcehealth.StateAvailable)
	return result, err
}

func wiiCatalogItems(games []model.Game) ([]store.CatalogItem, error) {
	items := make([]store.CatalogItem, 0, len(games))
	for _, game := range games {
		game.Availability = string(sourcehealth.AvailabilityPlayable)
		data, marshalErr := json.Marshal(game)
		if marshalErr != nil {
			return nil, marshalErr
		}
		items = append(items, store.CatalogItem{ID: game.ID, Payload: data})
	}
	return items, nil
}

func loadWiiCatalog(database *store.Store, record sourcehealth.Record) (scanner.Result, error) {
	items, err := database.Catalog("wii")
	if err != nil {
		return scanner.Result{}, err
	}
	if len(items) == 0 {
		// Migration bridge: an existing immutable Wii snapshot is authoritative
		// historical state and is safer than inventing an empty catalog.
		snapshot, activeErr := database.Active()
		if activeErr != nil || len(snapshot.Games) == 0 {
			return scanner.Result{}, errors.New("no complete Wii catalog is available")
		}
		games := append([]model.Game(nil), snapshot.Games...)
		for index := range games {
			games[index].Availability = string(
				sourcehealth.DerivedAvailability(record.State, sourcehealth.AvailabilityPlayable))
		}
		return scanner.Result{
			Games: games, Root: record.RootPath, Platform: "wii",
			ScanStatus: string(record.State),
		}, nil
	}
	games, err := decodeWiiItems(items, record.State)
	return scanner.Result{
		Games: games, Root: record.RootPath, Platform: "wii",
		ScanStatus: string(record.State),
	}, err
}

func decodeWiiItems(items []store.CatalogItem, state sourcehealth.State) ([]model.Game, error) {
	result := make([]model.Game, 0, len(items))
	for _, item := range items {
		var game model.Game
		if err := json.Unmarshal(item.Payload, &game); err != nil {
			return nil, err
		}
		game.Availability = string(sourcehealth.DerivedAvailability(state, item.Availability))
		result = append(result, game)
	}
	return result, nil
}

func scanGameCubeCatalog(database *store.Store, root string,
	record sourcehealth.Record, sharedRoot ...bool,
) (gamecube.Result, sourcehealth.Record, error) {
	if record.State != sourcehealth.StateAvailable {
		cached, err := loadGameCubeCatalog(database, record)
		if err != nil {
			return cached, record, err
		}
		code := record.FailureCode
		if code == "" {
			code = "SOURCE-OFFLINE"
		}
		return cached, record, errors.New(code + ": source is not available")
	}
	result, err := gamecube.Scan(root)
	if err != nil {
		record = sourcehealth.Partial(record, err)
		_ = database.UpsertSource(record)
		_ = database.RecordSourceEvent(record.SourceID, record.FailureCode, record.FailureMessage)
		cached, cacheErr := loadGameCubeCatalog(database, record)
		if cacheErr != nil {
			return gamecube.Result{}, record, err
		}
		return cached, record, err
	}
	successfulCount := len(result.Games)
	if len(sharedRoot) == 0 || sharedRoot[0] {
		if wiiItems, catalogErr := database.Catalog("wii"); catalogErr == nil {
			successfulCount += len(wiiItems)
		}
	}
	result, err = reconcileGameCubeResult(database, result)
	if err != nil {
		return gamecube.Result{}, record, err
	}
	record = sourcehealth.Successful(record, successfulCount)
	if err = database.UpsertSource(record); err != nil {
		return gamecube.Result{}, record, err
	}
	return result, record, nil
}

func reconcileGameCubeResult(database *store.Store,
	result gamecube.Result,
) (gamecube.Result, error) {
	items, err := gameCubeCatalogItems(result.Games)
	if err != nil {
		return gamecube.Result{}, err
	}
	reconciled, err := database.ReconcileCatalog("gamecube", items, 2)
	if err != nil {
		return gamecube.Result{}, err
	}
	result.Games, err = decodeGameCubeItems(reconciled, sourcehealth.StateAvailable)
	return result, err
}

func gameCubeCatalogItems(games []gamecube.Game) ([]store.CatalogItem, error) {
	items := make([]store.CatalogItem, 0, len(games))
	for _, game := range games {
		game.Availability = string(sourcehealth.AvailabilityPlayable)
		data, marshalErr := json.Marshal(game)
		if marshalErr != nil {
			return nil, marshalErr
		}
		items = append(items, store.CatalogItem{
			ID: fmt.Sprintf("%s:r%d", game.ID, game.Revision), Payload: data,
		})
	}
	return items, nil
}

func loadGameCubeCatalog(database *store.Store,
	record sourcehealth.Record,
) (gamecube.Result, error) {
	items, err := database.Catalog("gamecube")
	if err != nil {
		return gamecube.Result{}, err
	}
	games, err := decodeGameCubeItems(items, record.State)
	return gamecube.Result{
		Games: games, Root: record.RootPath, Platform: "gamecube",
		ScanStatus: string(record.State),
	}, err
}

func decodeGameCubeItems(items []store.CatalogItem,
	state sourcehealth.State,
) ([]gamecube.Game, error) {
	result := make([]gamecube.Game, 0, len(items))
	for _, item := range items {
		var game gamecube.Game
		if err := json.Unmarshal(item.Payload, &game); err != nil {
			return nil, err
		}
		game.Availability = string(sourcehealth.DerivedAvailability(state, item.Availability))
		result = append(result, game)
	}
	return result, nil
}

func (a *app) freshCompatibility(r *http.Request, operation compat.Operation) (
	bridgecontrol.Status, compat.Result, error,
) {
	hostDescriptor := a.hostDescriptor
	if hostDescriptor.SchemaVersion == 0 {
		hostDescriptor = compat.NewDescriptor(
			"host", "", "", version, gitCommit, buildTime, compat.HostCapabilities())
	}
	if a.pi == nil {
		result := compat.Unreachable(hostDescriptor, operation,
			"Raspberry Pi coordination is not configured.")
		return bridgecontrol.Status{}, result, errors.New("Pi coordination is not configured")
	}
	status, err := a.pi.Probe(r.Context())
	var result compat.Result
	if err != nil {
		result = compat.Unreachable(hostDescriptor, operation,
			"The Raspberry Pi did not answer the authenticated compatibility check.")
	} else {
		a.mu.RLock()
		expectedDevice := a.compatibility.Firmware.DeviceID
		a.mu.RUnlock()
		boardSupported := status.BoardOK
		result = compat.Evaluate(hostDescriptor, status.Compatibility, compat.EvaluateOptions{
			Operation: operation, ExpectedBoard: status.Board,
			ExpectedDeviceID: expectedDevice, BoardSupported: &boardSupported,
		})
	}
	a.mu.Lock()
	a.compatibility = result
	a.mu.Unlock()
	if a.store != nil {
		_ = a.store.SaveCompatibility(result)
	}
	if err != nil {
		return status, result, err
	}
	if result.Status == compat.StateBlocked {
		return status, result, errors.New("COMPAT-OPERATION-BLOCKED")
	}
	return status, result, nil
}

func (a *app) compatibilityAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		_, result, _ := a.freshCompatibility(r, compat.OperationStatus)
		status := http.StatusOK
		if result.Status == compat.StateUnreachable {
			status = http.StatusBadGateway
		}
		writeJSONStatus(w, status, result)
		return
	}
	a.mu.RLock()
	result := a.compatibility
	a.mu.RUnlock()
	if result.CheckedAt.IsZero() {
		result = compat.Result{
			Status: compat.StateUnknown, CheckedAt: time.Now().UTC(),
			Host: a.hostDescriptor, Operation: compat.OperationStatus,
		}
	} else {
		result = compat.MarkCachedStale(result, time.Now().UTC(), 30*time.Second)
	}
	writeJSON(w, result)
}

func (a *app) sourceStatusAPI(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	record := a.source
	sources := map[string]sourcehealth.Record{"wii": record, "gamecube": a.librarySourceLocked("gamecube")}
	wii, gameCube := append([]model.Game(nil), a.scan.Games...),
		append([]gamecube.Game(nil), a.gcScan.Games...)
	a.mu.RUnlock()
	affectedWii, affectedGameCube := 0, 0
	for _, game := range wii {
		if game.Availability != "" &&
			game.Availability != string(sourcehealth.AvailabilityPlayable) {
			affectedWii++
		}
	}
	for _, game := range gameCube {
		if game.Availability != "" &&
			game.Availability != string(sourcehealth.AvailabilityPlayable) {
			affectedGameCube++
		}
	}
	writeJSON(w, map[string]any{
		"source": record, "sources": sources, "affected_wii_games": affectedWii,
		"affected_gamecube_games": affectedGameCube,
	})
}

func (a *app) sourceDiagnosticAPI(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	record := a.source
	sources := map[string]sourcehealth.Record{"wii": record, "gamecube": a.librarySourceLocked("gamecube")}
	wii, gameCube := append([]model.Game(nil), a.scan.Games...),
		append([]gamecube.Game(nil), a.gcScan.Games...)
	a.mu.RUnlock()
	report := struct {
		GeneratedAt time.Time                      `json:"generatedAt"`
		Source      sourcehealth.Record            `json:"source"`
		Sources     map[string]sourcehealth.Record `json:"sources"`
		Wii         []struct {
			ID           string `json:"id"`
			Availability string `json:"availability"`
		} `json:"wii"`
		GameCube []struct {
			ID           string `json:"id"`
			Revision     byte   `json:"revision"`
			Availability string `json:"availability"`
		} `json:"gamecube"`
	}{GeneratedAt: time.Now().UTC(), Source: record, Sources: sources}
	for _, game := range wii {
		report.Wii = append(report.Wii, struct {
			ID           string `json:"id"`
			Availability string `json:"availability"`
		}{game.ID, game.Availability})
	}
	for _, game := range gameCube {
		report.GameCube = append(report.GameCube, struct {
			ID           string `json:"id"`
			Revision     byte   `json:"revision"`
			Availability string `json:"availability"`
		}{game.ID, game.Revision, game.Availability})
	}
	w.Header().Set("Content-Disposition",
		`attachment; filename="wiibridge-source-diagnostic.json"`)
	writeJSON(w, report)
}

func (a *app) acknowledgeSourceRemoval(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("confirm") != "acknowledge" {
		http.Error(w, "confirmed removal acknowledgement is required", http.StatusConflict)
		return
	}
	platform, id := r.FormValue("platform"), r.FormValue("id")
	if err := a.store.AcknowledgeMissing(platform, id); err != nil {
		http.Error(w, "confirmed removal could not be acknowledged", http.StatusConflict)
		return
	}
	respondAction(w, r, http.StatusOK, map[string]string{"status": "acknowledged"},
		"Confirmed source removal acknowledged.", "all")
}

func (a *app) saveStoreForAdministration() (
	*gamecube.SaveStore, bool, gamecube.LibraryManifest, error,
) {
	a.mu.RLock()
	activeStore := a.gcSaves
	a.mu.RUnlock()
	manifest, err := a.gcLibrary.ManagedActive()
	if err != nil {
		return nil, false, gamecube.LibraryManifest{}, err
	}
	if activeStore != nil {
		return activeStore, false, manifest, nil
	}
	saveStore, err := gamecube.OpenLibrarySaveStore(
		a.gcLibrary.Root(), manifest, a.metricsRegistry)
	return saveStore, true, manifest, err
}

func (a *app) gameCubeSaveStatus(w http.ResponseWriter, _ *http.Request) {
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	a.mu.RLock()
	selection, blocked := a.gcSaveSelection, a.gcSaveError
	a.mu.RUnlock()
	response := map[string]any{
		"format_version": gamecube.SaveOverlayFormatVersion,
		"selection":      selection,
		"blocked_reason": blocked,
		"statuses":       []gamecube.SaveStatus{},
		"backups":        map[string][]gamecube.BackupMetadata{},
	}
	if selection.Mode == gamecube.MemoryCardPhysical {
		writeJSON(w, response)
		return
	}
	saveStore, closeStore, manifest, err := a.saveStoreForAdministration()
	if err != nil {
		response["blocked_reason"] = boundedLogError(err)
		response["generation_id"] = ""
		writeJSONStatus(w, http.StatusConflict, response)
		return
	}
	if closeStore {
		defer saveStore.Close()
	}
	statuses := saveStore.Statuses()
	history := make(map[string][]gamecube.BackupMetadata, len(statuses))
	for _, status := range statuses {
		items, listErr := saveStore.ListBackups(status.ID)
		if listErr == nil {
			history[status.ID] = items
		}
	}
	response["generation_id"] = manifest.GenerationID
	response["statuses"] = statuses
	response["backups"] = history
	writeJSON(w, response)
}

func (a *app) configureGameCubeSaves(w http.ResponseWriter, r *http.Request) {
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	if a.exports.Platform() == "gamecube" {
		http.Error(w, "detach the GameCube export before changing memory-card mode",
			http.StatusConflict)
		return
	}
	mode := gamecube.MemoryCardMode(r.FormValue("mode"))
	cardSize, err := strconv.ParseInt(r.FormValue("card_size"), 10, 64)
	if err != nil {
		http.Error(w, "invalid memory-card size", http.StatusBadRequest)
		return
	}
	maximum, err := strconv.Atoi(r.FormValue("maximum_backups"))
	if err != nil {
		http.Error(w, "invalid backup retention", http.StatusBadRequest)
		return
	}
	interval, err := time.ParseDuration(r.FormValue("automatic_backup_interval"))
	if err != nil || interval < 0 {
		http.Error(w, "invalid automatic backup interval", http.StatusBadRequest)
		return
	}
	selection := gamecube.SaveSelection{
		FormatVersion: gamecube.SaveOverlayFormatVersion,
		Mode:          mode, CardSize: cardSize,
		SharedCardName:          strings.TrimSpace(r.FormValue("shared_card_name")),
		AutomaticCreation:       r.FormValue("automatic_creation") == "1",
		MaximumRetainedBackups:  maximum,
		AutomaticBackupInterval: int64(interval / time.Second),
	}
	if err = selection.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if mode.IsLibraryEmulated() {
		if _, compatibility, compatibilityErr := a.freshCompatibility(
			r, compat.OperationGameCubeEmulated); compatibilityErr != nil {
			writeJSONStatus(w, http.StatusConflict, compatibility)
			return
		}
	}
	a.mu.RLock()
	previous := a.gcSaveSelection
	a.mu.RUnlock()
	if err = a.gcLibrary.ConfigureSaves(selection); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	path := filepath.Join(a.dataDir, "gamecube", "save-settings.json")
	if err = gamecube.SaveSaveSelection(path, selection); err != nil {
		_ = a.gcLibrary.ConfigureSaves(previous)
		http.Error(w, "save settings could not be committed", http.StatusInternalServerError)
		return
	}
	a.mu.Lock()
	a.gcMode, a.gcSaveSelection, a.gcSaveError = mode, selection, ""
	if manifest, activeErr := a.gcLibrary.ManagedActive(); activeErr == nil {
		a.gcUpdate = manifest.Mode != mode
	}
	a.mu.Unlock()
	if a.store != nil {
		_ = a.store.RecordAuditEvent("gamecube_save_mode_changed", string(mode))
	}
	respondAction(w, r, http.StatusOK, selection,
		"GameCube memory-card settings saved; rebuild the GameCube library if prompted.",
		"gamecube")
}

func playableGameCubeGames(games []gamecube.Game) []gamecube.Game {
	result := make([]gamecube.Game, 0, len(games))
	for _, game := range games {
		if game.Availability == "" ||
			game.Availability == string(sourcehealth.AvailabilityPlayable) {
			result = append(result, game)
		}
	}
	return result
}

func (a *app) gameCubeSaveAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action == "mode" {
		a.configureGameCubeSaves(w, r)
		return
	}
	if action != "create" && action != "backup" &&
		action != "verify" && action != "restore" {
		http.Error(w, "unsupported save action", http.StatusBadRequest)
		return
	}
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	if action == "create" {
		a.mu.RLock()
		selection := a.gcSaveSelection
		games := playableGameCubeGames(append([]gamecube.Game(nil), a.gcScan.Games...))
		a.mu.RUnlock()
		if !selection.Mode.IsLibraryEmulated() {
			http.Error(w, "select an emulated memory-card mode first", http.StatusConflict)
			return
		}
		objects, err := a.gcLibrary.EnsureSaveObjects(games)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if a.store != nil {
			_ = a.store.RecordAuditEvent(
				"gamecube_save_cards_created", strconv.Itoa(len(objects)))
		}
		respondAction(w, r, http.StatusCreated, objects,
			"Managed GameCube memory cards are ready.", "gamecube")
		return
	}
	if (action == "restore") && a.exports.Platform() == "gamecube" {
		http.Error(w, "detach the GameCube export before restore", http.StatusConflict)
		return
	}
	saveStore, closeStore, _, err := a.saveStoreForAdministration()
	if err != nil {
		http.Error(w, boundedLogError(err), http.StatusConflict)
		return
	}
	if closeStore {
		defer saveStore.Close()
	}
	objectID := r.FormValue("object_id")
	if objectID == "" {
		statuses := saveStore.Statuses()
		if len(statuses) == 1 {
			objectID = statuses[0].ID
		}
	}
	switch action {
	case "backup":
		if objectID == "" {
			for _, status := range saveStore.Statuses() {
				if _, err = saveStore.Backup(status.ID, "manual"); err != nil {
					break
				}
			}
		} else {
			_, err = saveStore.Backup(objectID, "manual")
		}
	case "verify":
		if objectID == "" {
			for _, status := range saveStore.Statuses() {
				if _, err = saveStore.Verify(status.ID); err != nil {
					break
				}
			}
		} else {
			_, err = saveStore.Verify(objectID)
		}
	case "restore":
		if r.FormValue("confirm") != "restore" {
			http.Error(w, "explicit restore confirmation is required", http.StatusBadRequest)
			return
		}
		if objectID == "" {
			http.Error(w, "save object is required", http.StatusBadRequest)
			return
		}
		err = saveStore.Restore(objectID, r.FormValue("backup"))
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if a.store != nil {
		_ = a.store.RecordAuditEvent("gamecube_save_"+action, objectID)
	}
	respondAction(w, r, http.StatusOK,
		map[string]any{"status": action + "-complete", "cards": saveStore.Statuses()},
		"GameCube save "+action+" completed.", "gamecube")
}

func (a *app) uploadGameCubeSave(w http.ResponseWriter, r *http.Request) {
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	if a.exports.Platform() == "gamecube" {
		http.Error(w, "detach the GameCube export before upload", http.StatusConflict)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, gamecube.MaximumSaveUploadSize+(128<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "a multipart card upload is required", http.StatusBadRequest)
		return
	}
	objectID := strings.TrimSpace(r.URL.Query().Get("object_id"))
	if objectID == "" {
		http.Error(w, "save object is required", http.StatusBadRequest)
		return
	}
	var card io.Reader
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		if part.FormName() == "card" {
			card = part
			defer part.Close()
			break
		}
		_ = part.Close()
	}
	if card == nil {
		http.Error(w, "card upload is missing", http.StatusBadRequest)
		return
	}
	saveStore, closeStore, _, err := a.saveStoreForAdministration()
	if err != nil {
		http.Error(w, boundedLogError(err), http.StatusConflict)
		return
	}
	if closeStore {
		defer saveStore.Close()
	}
	if err = saveStore.UploadStream(objectID, card); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if a.store != nil {
		_ = a.store.RecordAuditEvent("gamecube_save_uploaded", objectID)
	}
	writeJSON(w, map[string]string{"status": "uploaded", "object_id": objectID})
}

func (a *app) downloadGameCubeSave(w http.ResponseWriter, r *http.Request) {
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	saveStore, closeStore, _, err := a.saveStoreForAdministration()
	if err != nil {
		http.Error(w, boundedLogError(err), http.StatusConflict)
		return
	}
	if closeStore {
		defer saveStore.Close()
	}
	file, filename, err := saveStore.OpenDownload(
		r.URL.Query().Get("object_id"), r.URL.Query().Get("backup"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "save download unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+strings.ReplaceAll(filename, `"`, "")+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	_, _ = io.CopyN(w, file, info.Size())
}

func (a *app) runAutomaticSaveBackups(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		a.mu.RLock()
		selection := a.gcSaveSelection
		saveStore := a.gcSaves
		a.mu.RUnlock()
		interval := time.Duration(selection.AutomaticBackupInterval) * time.Second
		if interval <= 0 || saveStore == nil || !selection.Mode.IsLibraryEmulated() {
			continue
		}
		now := time.Now()
		for _, status := range saveStore.Statuses() {
			if !status.LastBackup.IsZero() && now.Sub(status.LastBackup) < interval {
				continue
			}
			if _, err := saveStore.Backup(status.ID, "automatic"); err != nil {
				continue
			}
			if a.store != nil {
				_ = a.store.RecordAuditEvent("gamecube_save_automatic_backup", status.ID)
			}
		}
	}
}

func (a *app) syncGameCubeSaves() error {
	a.mu.RLock()
	saveStore := a.gcSaves
	a.mu.RUnlock()
	if saveStore == nil {
		return nil
	}
	return saveStore.Sync()
}

func (a *app) queueSourceFailure(code string) {
	if a.sourceFailures == nil {
		return
	}
	select {
	case a.sourceFailures <- code:
	default:
	}
}

func (a *app) runSourceFailureReconciler(ctx context.Context) {
	lastRecorded := make(map[string]time.Time, 8)
	for {
		select {
		case <-ctx.Done():
			return
		case value := <-a.sourceFailures:
			platform, code := sourceFailurePlatform(value)
			if !shouldRecordSourceFailure(lastRecorded, platform+":"+code, time.Now()) {
				continue
			}
			a.scanMu.Lock()
			a.mu.RLock()
			previous := a.librarySourceLocked(platform)
			a.mu.RUnlock()
			preflight, err := sourcehealth.Preflight(a.libraryRoot(platform), &previous)
			record := preflight.Record
			if err == nil {
				record = sourcehealth.RuntimeFailure(record, code)
			}
			a.recordLibraryFailure([]string{platform}, record)
			if a.store != nil {
				_ = a.store.RecordSourceEvent(record.SourceID, record.FailureCode, record.FailureMessage)
			}
			a.scanMu.Unlock()
		}
	}
}

func shouldRecordSourceFailure(last map[string]time.Time, code string, now time.Time) bool {
	if previous, exists := last[code]; exists && now.Sub(previous) < 30*time.Second {
		return false
	}
	last[code] = now
	return true
}

func normalizedSourceFailureCode(code string) string {
	switch code {
	case "SOURCE-READ-FAILED", "SOURCE-IDENTITY-CHANGED",
		"SOURCE-PERMISSION-DENIED", "SOURCE-MOUNT-MISSING":
		return code
	default:
		return "SOURCE-READ-FAILED"
	}
}

func (a *app) runMetricsPersistence(ctx context.Context) {
	if a.metricsRegistry == nil || !a.metricsRegistry.Enabled() ||
		a.metricsPersistence <= 0 {
		return
	}
	ticker := time.NewTicker(a.metricsPersistence)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.persistMetricsSnapshot(); err != nil {
				a.metricsRegistry.SavePersistenceFailure()
			}
		}
	}
}

func (a *app) persistMetricsSnapshot() error {
	if a.metricsRegistry == nil || !a.metricsRegistry.Enabled() {
		return nil
	}
	directory := filepath.Join(a.dataDir, "performance")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("PERF-PERSISTENCE-FAILED: invalid metrics directory")
	}
	data, err := json.MarshalIndent(a.performanceSnapshot(), "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(directory, "current.json")
	temp := filepath.Join(directory, ".current.json.tmp")
	if stale, staleErr := os.Lstat(temp); staleErr == nil {
		if !stale.Mode().IsRegular() || stale.Mode()&os.ModeSymlink != 0 {
			return errors.New("PERF-PERSISTENCE-FAILED: unsafe metrics checkpoint")
		}
		if err = os.Remove(temp); err != nil {
			return err
		}
	} else if !errors.Is(staleErr, os.ErrNotExist) {
		return staleErr
	}
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writeErr := error(nil)
	if _, writeErr = file.Write(append(data, '\n')); writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(temp)
		return writeErr
	}
	if err = os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := directoryFile.Sync()
	closeErr := directoryFile.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (a *app) performanceSnapshot() perf.Snapshot {
	openFiles := int64(0)
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		openFiles = int64(len(entries))
	}
	a.mu.RLock()
	phase, readiness := a.gcStartupPhase, "ready"
	if !a.ready {
		readiness = "not-ready"
	}
	a.mu.RUnlock()
	if a.metricsRegistry == nil {
		return perf.Snapshot{UpdatedAt: time.Now().UTC()}
	}
	return a.metricsRegistry.Snapshot(phase, readiness, openFiles, a.memoryLimit)
}

func (a *app) performanceSummary(w http.ResponseWriter, _ *http.Request) {
	snapshot := a.performanceSnapshot()
	var piMetrics any = nil
	piState := "unavailable"
	if value, err := a.performancePiMetrics(); err == nil {
		piMetrics, piState = value, "available"
	}
	platform := a.exports.Platform()
	a.mu.RLock()
	source, compatibility := a.librarySourceLocked(platform), a.compatibility
	a.mu.RUnlock()
	current, active := perf.SessionSummary{}, false
	warnings := []contract.Error{}
	if a.metricsRegistry != nil {
		current, active = a.metricsRegistry.CurrentSession()
		warnings = a.metricsRegistry.Warnings(snapshot)
	}
	if source.State != sourcehealth.StateAvailable {
		warnings = append(warnings, contract.New(
			"SOURCE-OFFLINE", "source", contract.SeverityWarning,
			"The source is unavailable; this warning does not prove the underlying cause.", true))
	}
	if piValue, ok := piMetrics.(perf.PiSnapshot); ok {
		if piValue.MemoryTotalBytes > 0 &&
			piValue.MemoryUsedBytes*100 >= piValue.MemoryTotalBytes*90 {
			warnings = append(warnings, contract.New(
				"PERF-PI-MEMORY-HIGH", "firmware", contract.SeverityWarning,
				"Pi memory utilization exceeds the configured diagnostic threshold.", true))
		}
		if piValue.TemperatureCelsius >= 80 {
			warnings = append(warnings, contract.New(
				"PERF-PI-TEMPERATURE-HIGH", "firmware", contract.SeverityWarning,
				"Pi temperature exceeds the diagnostic warning threshold.", true))
		}
		if piValue.USBResetCount >= 3 {
			warnings = append(warnings, contract.New(
				"PERF-USB-RESETS-HIGH", "usb", contract.SeverityWarning,
				"Repeated USB gadget resets were observed.", true))
		}
	} else if compatibility.Firmware.SchemaVersion != 0 &&
		containsCapability(compatibility.Firmware.Capabilities, compat.CapRuntimeMetrics) {
		warnings = append(warnings, contract.New(
			"PERF-PI-METRICS-UNAVAILABLE", "firmware", contract.SeverityWarning,
			"Pi runtime metrics are temporarily unavailable; game serving is unaffected.", true))
	}
	piSnapshot, piAvailable := piMetrics.(perf.PiSnapshot)
	piUpdated := time.Time{}
	piThroughput, piErrors, usbErrors := float64(0), int64(0), int64(0)
	if piAvailable {
		piUpdated = piSnapshot.UpdatedAt
		piThroughput = piSnapshot.NBDReadBytesPerSecond
		piErrors = int64(piSnapshot.NBDReadFailures)
		usbErrors = int64(piSnapshot.USBResetCount)
	}
	writeJSON(w, map[string]any{
		"host": snapshot, "pi": piMetrics, "pi_state": piState,
		"source": source, "compatibility": compatibility,
		"session": current, "session_active": active,
		"warnings": warnings,
		"data_path": []map[string]any{
			{"stage": "Source", "state": source.State, "measurement": "measured",
				"throughput_bytes_per_second": snapshot.Source.Rates.BytesPerSecond1m,
				"latency_p99_us":              snapshot.Source.Latency.P99US,
				"error_count":                 snapshot.Source.Counters["read_errors"],
				"last_update":                 snapshot.UpdatedAt},
			{"stage": "Host backend", "state": "available", "measurement": "estimated",
				"throughput_bytes_per_second": snapshot.NBD.Rates.BytesPerSecond1m,
				"latency_p99_us":              snapshot.Disk.Latency.P99US,
				"error_count":                 snapshot.Source.Counters["identity_check_failures"],
				"last_update":                 snapshot.UpdatedAt},
			{"stage": "NBD/TLS", "state": a.exports.State(), "measurement": "measured",
				"throughput_bytes_per_second": snapshot.NBD.Rates.BytesPerSecond1m,
				"latency_p99_us":              snapshot.NBD.Latency.P99US,
				"error_count": snapshot.NBD.Counters["protocol_errors"] +
					snapshot.NBD.Counters["timeouts"] +
					snapshot.NBD.Counters["tls_failures"],
				"last_update": snapshot.UpdatedAt},
			{"stage": "Pi NBD", "state": piState,
				"measurement": func() string {
					if piAvailable {
						return "estimated"
					}
					return "unavailable"
				}(),
				"throughput_bytes_per_second": piThroughput,
				"latency_p99_us":              nil, "error_count": piErrors,
				"last_update": piUpdated},
			{"stage": "USB gadget", "state": func() string {
				if piAvailable {
					return piSnapshot.USBState
				}
				return "unavailable"
			}(), "measurement": "state-only",
				"throughput_bytes_per_second": nil, "latency_p99_us": nil,
				"error_count": usbErrors, "last_update": piUpdated},
			{"stage": "Wii", "state": "not directly observable",
				"measurement": "unavailable", "throughput_bytes_per_second": nil,
				"latency_p99_us": nil, "error_count": nil,
				"last_update": time.Time{}},
		},
	})
}

func containsCapability(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func (a *app) performancePiMetrics() (perf.PiSnapshot, error) {
	if a.pi == nil {
		return perf.PiSnapshot{}, errors.New("PERF-PI-METRICS-UNAVAILABLE")
	}
	provider, ok := a.pi.(interface {
		Metrics(context.Context) (perf.PiSnapshot, error)
	})
	if !ok {
		return perf.PiSnapshot{}, errors.New("PERF-PI-METRICS-UNAVAILABLE")
	}
	snapshot, err := provider.Metrics(context.Background())
	if err != nil {
		return perf.PiSnapshot{}, err
	}
	a.mu.Lock()
	delta := uint64(0)
	if snapshot.USBResetCount >= a.lastPiUSBResets {
		delta = snapshot.USBResetCount - a.lastPiUSBResets
	} else {
		// A lower counter means the firmware restarted. The new lifetime
		// counter is the only safe bounded delta available.
		delta = snapshot.USBResetCount
	}
	a.lastPiUSBResets = snapshot.USBResetCount
	a.mu.Unlock()
	if delta > 0 && a.metricsRegistry != nil {
		a.metricsRegistry.RecordUSBResets(delta)
	}
	return snapshot, nil
}

func (a *app) performanceHost(w http.ResponseWriter, _ *http.Request) {
	snapshot := a.performanceSnapshot()
	var warnings any = []any{}
	if a.metricsRegistry != nil {
		warnings = a.metricsRegistry.Warnings(snapshot)
	}
	writeJSON(w, map[string]any{
		"metrics": snapshot, "warnings": warnings,
	})
}

func (a *app) performancePi(w http.ResponseWriter, _ *http.Request) {
	value, err := a.performancePiMetrics()
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"available": false, "code": "PERF-PI-METRICS-UNAVAILABLE",
		})
		return
	}
	writeJSON(w, value)
}

func (a *app) performanceCurrentSession(w http.ResponseWriter, _ *http.Request) {
	if a.metricsRegistry == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"status": "metrics-disabled"})
		return
	}
	session, ok := a.metricsRegistry.CurrentSession()
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"status": "no-active-session"})
		return
	}
	writeJSON(w, session)
}

func (a *app) performanceSessions(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	var sessions []perf.SessionSummary
	if a.metricsRegistry != nil {
		sessions = a.metricsRegistry.Sessions(offset, limit)
	}
	writeJSON(w, map[string]any{
		"offset": offset, "limit": limit,
		"sessions": sessions,
	})
}

func (a *app) performanceSession(w http.ResponseWriter, r *http.Request) {
	if a.metricsRegistry == nil {
		http.Error(w, "performance metrics are disabled", http.StatusNotFound)
		return
	}
	id := r.PathValue("id")
	if current, ok := a.metricsRegistry.CurrentSession(); ok && current.ID == id {
		writeJSON(w, current)
		return
	}
	if session, ok := a.metricsRegistry.Session(id); ok {
		writeJSON(w, session)
		return
	}
	http.Error(w, "performance session not found", http.StatusNotFound)
}

func (a *app) performanceExport(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	var sessions []perf.SessionSummary
	if a.metricsRegistry != nil {
		sessions = a.metricsRegistry.Sessions(0, 100)
	}
	if format == "" || format == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="wiibridge-performance.json"`)
		writeJSON(w, map[string]any{
			"generated_at": time.Now().UTC(), "summary": a.performanceSnapshot(),
			"sessions": sessions,
		})
		return
	}
	if format != "csv" {
		http.Error(w, "format must be json or csv", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="wiibridge-performance.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{
		"session_id", "start", "end", "platform", "total_bytes", "read_count",
		"p95_us", "p99_us", "maximum_us", "source_errors", "nbd_disconnects",
		"usb_resets", "save_flushes", "outcome",
	})
	for _, session := range sessions {
		_ = writer.Write([]string{
			session.ID, session.Start.Format(time.RFC3339Nano),
			session.End.Format(time.RFC3339Nano), session.Platform,
			strconv.FormatUint(session.TotalBytes, 10),
			strconv.FormatUint(session.ReadCount, 10),
			strconv.FormatFloat(session.P95LatencyUS, 'f', 3, 64),
			strconv.FormatFloat(session.P99LatencyUS, 'f', 3, 64),
			strconv.FormatFloat(session.MaximumLatencyUS, 'f', 3, 64),
			strconv.FormatUint(session.SourceErrors, 10),
			strconv.FormatUint(session.NBDDisconnects, 10),
			strconv.FormatUint(session.USBResets, 10),
			strconv.FormatUint(session.SaveFlushes, 10), session.Outcome,
		})
	}
	writer.Flush()
}

func parseByteSize(value string) int64 {
	value = strings.TrimSpace(strings.ToLower(value))
	multiplier := int64(1)
	for suffix, factor := range map[string]int64{
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30,
		"k": 1000, "m": 1000 * 1000, "g": 1000 * 1000 * 1000,
	} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
			multiplier = factor
			break
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < 0 || number > (1<<63-1)/multiplier {
		return 0
	}
	return number * multiplier
}

func detectContainerMemoryLimit(fallback int64) int64 {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(data))
		if value == "max" {
			return fallback
		}
		limit, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr == nil && limit > 0 && limit < 1<<60 {
			return limit
		}
	}
	return fallback
}

func (a *app) startPerformanceSession() {
	if a.metricsRegistry == nil {
		return
	}
	a.mu.RLock()
	compatibility := a.compatibility
	a.mu.RUnlock()
	firmwareVersion := compatibility.Firmware.ProductVersion
	protocol := 0
	if compatibility.NegotiatedProtocol != nil {
		protocol = *compatibility.NegotiatedProtocol
	}
	a.metricsRegistry.StartSession(
		a.exports.Platform(), "", version, firmwareVersion, protocol)
}

func (a *app) endPerformanceSession(outcome string) {
	if a.metricsRegistry == nil {
		return
	}
	if summary, ok := a.metricsRegistry.EndSession(outcome); ok {
		if a.store != nil {
			if err := a.store.SavePerformanceSession(
				summary, a.maxSessions, a.sessionRetentionDays); err == nil {
				return
			}
			// Metrics persistence is deliberately non-fatal to game serving.
			a.metricsRegistry.SavePersistenceFailure()
		}
	}
}
