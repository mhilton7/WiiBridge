package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"wiibridge/shared/sourcehealth"
)

func TestSchemaTwoMigrationPreservesCatalogAndLegacyTrust(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r := sourcehealth.Record{SourceID: "legacy", RootPath: "/library", State: sourcehealth.StateAvailable, LastKnownDevice: 146, LastKnownMountInfo: "old-mount", LastAttemptedScan: time.Now().UTC(), LastSuccessfulItemCount: 1}
	if err = s.UpsertSource(r); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReconcileCatalog("wii", []CatalogItem{{ID: "SWII01", Payload: []byte(`{"id":"SWII01"}`)}}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`ALTER TABLE source_roots DROP COLUMN filesystem_id; ALTER TABLE source_roots DROP COLUMN root_inode; DELETE FROM schema_migrations WHERE version=3;`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.SourceByRoot(r.RootPath)
	if err != nil || got.LastKnownDevice != 146 || got.LastKnownMountInfo != "old-mount" || got.FilesystemID != "" {
		t.Fatalf("legacy baseline lost: %#v %v", got, err)
	}
	items, err := s.Catalog("wii")
	if err != nil || len(items) != 1 || items[0].ID != "SWII01" {
		t.Fatal("catalog lost during migration")
	}
	if _, err = os.Stat(path + ".pre-schema3.bak"); err != nil {
		t.Fatal("rollback backup missing", err)
	}
	got.FilesystemID, got.RootInode = "zfs:1234567890abcdef", 42
	if err = s.UpsertSource(got); err != nil {
		t.Fatal(err)
	}
	got, err = s.SourceByRoot(r.RootPath)
	if err != nil || got.FilesystemID != "zfs:1234567890abcdef" || got.RootInode != 42 {
		t.Fatal("stable identity not persisted")
	}
}
