package store

import (
	"errors"
	"path/filepath"
	"testing"

	"wiibridge/shared/model"
	"wiibridge/shared/sourcehealth"
)

func TestSourceScanValidationFailureRollsBackIdentityAndCatalogs(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	old := sourcehealth.Successful(sourcehealth.Record{SourceID: "old-source", RootPath: "/library", LastKnownDevice: 11, LastKnownMountInfo: "old-mount"}, 1)
	if err = database.UpsertSource(old); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ReconcileCatalog("gamecube", []CatalogItem{{ID: "GOLD01", Payload: []byte(`{"path":"old.iso"}`)}}, 2); err != nil {
		t.Fatal(err)
	}
	replacement := old
	replacement.SourceID, replacement.LastKnownDevice, replacement.LastKnownMountInfo = "new-source", 12, "new-mount"
	rejected := errors.New("synthetic snapshot validation failure")
	err = database.CommitSourceScan(replacement, map[string][]CatalogItem{
		"gamecube": {{ID: "GNEW01", Payload: []byte(`{"path":"new.iso"}`)}},
	}, func(catalogs map[string][]CatalogItem) (*model.Snapshot, error) {
		if len(catalogs["gamecube"]) != 2 {
			t.Fatal("validation did not receive reconciled tombstone and new item")
		}
		return nil, rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatalf("validation failure lost: %v", err)
	}
	actual, _ := database.SourceByRoot("/library")
	items, _ := database.Catalog("gamecube")
	if actual.SourceID != old.SourceID || actual.LastKnownDevice != old.LastKnownDevice || len(items) != 1 || items[0].ID != "GOLD01" || items[0].MissingObservations != 0 {
		t.Fatalf("failed transaction changed trusted data: source=%#v items=%#v", actual, items)
	}
}
