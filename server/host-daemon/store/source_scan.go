package store

import (
	"errors"
	"time"

	"wiibridge/shared/model"
	"wiibridge/shared/sourcehealth"
)

// CommitSourceScan publishes a complete scan and its trusted mount identity in
// one transaction. prepare validates the reconciled catalog before anything is
// committed and optionally supplies the new authoritative Wii snapshot.
func (s *Store) CommitSourceScan(record sourcehealth.Record,
	catalogs map[string][]CatalogItem,
	prepare func(map[string][]CatalogItem) (*model.Snapshot, error),
) error {
	if len(catalogs) == 0 || record.State != sourcehealth.StateAvailable {
		return errors.New("complete available source scan required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	reconciled := make(map[string][]CatalogItem, len(catalogs))
	for platform, items := range catalogs {
		if err = reconcileCatalogTx(tx, platform, items, 2, now); err != nil {
			return err
		}
		if reconciled[platform], err = readCatalog(tx, platform); err != nil {
			return err
		}
	}
	snapshot, err := prepare(reconciled)
	if err != nil {
		return err
	}
	if snapshot != nil {
		if err = publishSnapshotTx(tx, *snapshot); err != nil {
			return err
		}
	}
	if err = upsertSource(tx, record); err != nil {
		return err
	}
	return tx.Commit()
}
