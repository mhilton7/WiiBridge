// Package store owns the versioned SQLite control-plane database.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"wiibridge/shared/model"
	"wiibridge/shared/perf"
	"wiibridge/shared/sourcehealth"

	_ "modernc.org/sqlite"
)

type Store struct {
	db          *sql.DB
	path        string
	preexisting bool
}

func Open(path string) (*Store, error) {
	preexisting := false
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("persistent database is not a regular file")
		}
		preexisting = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db, path: path, preexisting: preexisting}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const schema1 = `
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_utc TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS snapshots(
 snapshot_id TEXT PRIMARY KEY, catalog_id TEXT NOT NULL,
 virtual_disk_size INTEGER NOT NULL, metadata_hash TEXT NOT NULL,
 manifest_json BLOB NOT NULL, healthy INTEGER NOT NULL DEFAULT 1,
 created_utc TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS active_snapshot(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 snapshot_id TEXT NOT NULL REFERENCES snapshots(snapshot_id));
CREATE TABLE IF NOT EXISTS audit_events(
 id INTEGER PRIMARY KEY AUTOINCREMENT, event_type TEXT NOT NULL,
 detail TEXT NOT NULL, created_utc TEXT NOT NULL);
INSERT OR IGNORE INTO schema_migrations(version,applied_utc)
VALUES(1,strftime('%Y-%m-%dT%H:%M:%fZ','now'));`
	if _, err := s.db.Exec(schema1); err != nil {
		return err
	}
	var schema2AlreadyApplied bool
	var schema2Count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE version=2`).Scan(&schema2Count); err != nil {
		return err
	}
	schema2AlreadyApplied = schema2Count != 0
	if !schema2AlreadyApplied && s.preexisting {
		if _, err := s.db.Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
			return err
		}
		if err := backupPreSchema2Database(s.path); err != nil {
			return fmt.Errorf("back up schema-1 database: %w", err)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const schema2 = `
CREATE TABLE IF NOT EXISTS source_roots(
 source_id TEXT NOT NULL,
 root_path TEXT PRIMARY KEY,
 state TEXT NOT NULL,
 last_successful_scan TEXT,
 last_attempted_scan TEXT NOT NULL,
 last_successful_item_count INTEGER NOT NULL DEFAULT 0,
 failure_code TEXT NOT NULL DEFAULT '',
 failure_message TEXT NOT NULL DEFAULT '',
 consecutive_failures INTEGER NOT NULL DEFAULT 0,
 last_known_device INTEGER NOT NULL DEFAULT 0,
 last_known_filesystem TEXT NOT NULL DEFAULT '',
 last_known_mount_info TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS catalog_items(
 platform TEXT NOT NULL,
 item_id TEXT NOT NULL,
 payload_json BLOB NOT NULL,
 availability TEXT NOT NULL DEFAULT 'playable',
 missing_observations INTEGER NOT NULL DEFAULT 0,
 last_seen_utc TEXT NOT NULL,
 missing_confirmed_utc TEXT,
 PRIMARY KEY(platform,item_id));
CREATE TABLE IF NOT EXISTS source_events(
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 source_id TEXT NOT NULL,
 code TEXT NOT NULL,
 message TEXT NOT NULL,
 created_utc TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS compatibility_cache(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 result_json BLOB NOT NULL,
 checked_utc TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS performance_sessions(
 session_id TEXT PRIMARY KEY,
 start_utc TEXT NOT NULL,
 end_utc TEXT NOT NULL,
 summary_json BLOB NOT NULL);
INSERT OR IGNORE INTO schema_migrations(version,applied_utc)
VALUES(2,strftime('%Y-%m-%dT%H:%M:%fZ','now'));`
	if _, err = tx.Exec(schema2); err != nil {
		return err
	}
	if !schema2AlreadyApplied {
		if err = seedLegacyWiiCatalog(tx); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.migrateSourceIdentity()
}

func backupPreSchema2Database(path string) error {
	sourceInfo, err := os.Lstat(path)
	if err != nil || !sourceInfo.Mode().IsRegular() ||
		sourceInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("schema-1 database is not a regular file")
	}
	return backupDatabase(path, ".pre-schema2.bak")
}

func backupDatabase(path, suffix string) error {
	sourceInfo, err := os.Lstat(path)
	if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("database backup source is unsafe")
	}
	backupPath := path + suffix
	if info, statErr := os.Lstat(backupPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("database rollback backup is unsafe")
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	tempPath := backupPath + ".tmp"
	if info, statErr := os.Lstat(tempPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("database rollback staging file is unsafe")
		}
		if err = os.Remove(tempPath); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	copyErr := error(nil)
	if _, copyErr = io.Copy(target, source); copyErr == nil {
		copyErr = target.Sync()
	}
	if closeErr := target.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return copyErr
	}
	if err = os.Rename(tempPath, backupPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func seedLegacyWiiCatalog(tx *sql.Tx) error {
	var manifest []byte
	err := tx.QueryRow(`SELECT s.manifest_json FROM snapshots s
 JOIN active_snapshot a ON a.snapshot_id=s.snapshot_id
 WHERE a.singleton=1 AND s.healthy=1`).Scan(&manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot model.Snapshot
	if err = json.Unmarshal(manifest, &snapshot); err != nil {
		return fmt.Errorf("decode legacy Wii snapshot for migration: %w", err)
	}
	seenAt := snapshot.Created
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	for _, game := range snapshot.Games {
		if game.ID == "" {
			return errors.New("legacy Wii snapshot contains an invalid game")
		}
		game.Availability = string(sourcehealth.AvailabilityPlayable)
		payload, marshalErr := json.Marshal(game)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO catalog_items(
 platform,item_id,payload_json,availability,missing_observations,last_seen_utc,
 missing_confirmed_utc) VALUES('wii',?,?,'playable',0,?,NULL)`,
			game.ID, payload, seenAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Publish(snapshot model.Snapshot) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = publishSnapshotTx(tx, snapshot); err != nil {
		return err
	}
	return tx.Commit()
}

func publishSnapshotTx(tx *sql.Tx, snapshot model.Snapshot) error {
	manifest, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO snapshots
 (snapshot_id,catalog_id,virtual_disk_size,metadata_hash,manifest_json,created_utc)
 VALUES(?,?,?,?,?,?)`, snapshot.SnapshotID, snapshot.CatalogID,
		snapshot.VirtualDiskSize, snapshot.MetadataHash, manifest,
		snapshot.Created.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO active_snapshot(singleton,snapshot_id) VALUES(1,?)
 ON CONFLICT(singleton) DO UPDATE SET snapshot_id=excluded.snapshot_id`,
		snapshot.SnapshotID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO audit_events(event_type,detail,created_utc)
 VALUES('snapshot_published',?,?)`, snapshot.SnapshotID,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return nil
}

func (s *Store) Active() (model.Snapshot, error) {
	var manifest []byte
	err := s.db.QueryRow(`SELECT s.manifest_json FROM snapshots s
 JOIN active_snapshot a ON a.snapshot_id=s.snapshot_id
 WHERE a.singleton=1 AND s.healthy=1`).Scan(&manifest)
	if err != nil {
		return model.Snapshot{}, err
	}
	var snapshot model.Snapshot
	err = json.Unmarshal(manifest, &snapshot)
	return snapshot, err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SourceByRoot(root string) (sourcehealth.Record, error) {
	var record sourcehealth.Record
	var successful sql.NullString
	var attempted string
	err := s.db.QueryRow(`SELECT source_id,root_path,state,last_successful_scan,
 last_attempted_scan,last_successful_item_count,failure_code,failure_message,
 consecutive_failures,last_known_device,last_known_filesystem,last_known_mount_info,filesystem_id,root_inode
 FROM source_roots WHERE root_path=?`, root).Scan(
		&record.SourceID, &record.RootPath, &record.State, &successful, &attempted,
		&record.LastSuccessfulItemCount, &record.FailureCode, &record.FailureMessage,
		&record.ConsecutiveFailures, &record.LastKnownDevice,
		&record.LastKnownFilesystem, &record.LastKnownMountInfo, &record.FilesystemID, &record.RootInode)
	if err != nil {
		return sourcehealth.Record{}, err
	}
	if successful.Valid {
		record.LastSuccessfulScan, _ = time.Parse(time.RFC3339Nano, successful.String)
	}
	record.LastAttemptedScan, _ = time.Parse(time.RFC3339Nano, attempted)
	return record, nil
}

func (s *Store) UpsertSource(record sourcehealth.Record) error {
	return upsertSource(s.db, record)
}

type sqlExecutor interface {
	Exec(string, ...any) (sql.Result, error)
}

func upsertSource(executor sqlExecutor, record sourcehealth.Record) error {
	if record.SourceID == "" || record.RootPath == "" || record.LastAttemptedScan.IsZero() {
		return errors.New("invalid source state")
	}
	var successful any
	if !record.LastSuccessfulScan.IsZero() {
		successful = record.LastSuccessfulScan.UTC().Format(time.RFC3339Nano)
	}
	_, err := executor.Exec(`INSERT INTO source_roots(
 source_id,root_path,state,last_successful_scan,last_attempted_scan,
 last_successful_item_count,failure_code,failure_message,consecutive_failures,
 last_known_device,last_known_filesystem,last_known_mount_info,filesystem_id,root_inode)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(root_path) DO UPDATE SET
 source_id=excluded.source_id,state=excluded.state,
 last_successful_scan=excluded.last_successful_scan,
 last_attempted_scan=excluded.last_attempted_scan,
 last_successful_item_count=excluded.last_successful_item_count,
 failure_code=excluded.failure_code,failure_message=excluded.failure_message,
 consecutive_failures=excluded.consecutive_failures,
 last_known_device=excluded.last_known_device,
 last_known_filesystem=excluded.last_known_filesystem,
 last_known_mount_info=excluded.last_known_mount_info,
 filesystem_id=excluded.filesystem_id,root_inode=excluded.root_inode`,
		record.SourceID, record.RootPath, record.State, successful,
		record.LastAttemptedScan.UTC().Format(time.RFC3339Nano),
		record.LastSuccessfulItemCount, record.FailureCode, bounded(record.FailureMessage, 240),
		record.ConsecutiveFailures, record.LastKnownDevice,
		record.LastKnownFilesystem, record.LastKnownMountInfo, record.FilesystemID, record.RootInode)
	return err
}

type CatalogItem struct {
	Platform            string                    `json:"platform"`
	ID                  string                    `json:"id"`
	Payload             json.RawMessage           `json:"payload"`
	Availability        sourcehealth.Availability `json:"availability"`
	MissingObservations int                       `json:"missing_observations"`
	LastSeen            time.Time                 `json:"last_seen"`
	MissingConfirmed    time.Time                 `json:"missing_confirmed,omitempty"`
}

func (s *Store) ReconcileCatalog(platform string, current []CatalogItem,
	confirmationThreshold int,
) ([]CatalogItem, error) {
	if platform != "wii" && platform != "gamecube" {
		return nil, errors.New("invalid catalog platform")
	}
	if confirmationThreshold < 1 {
		confirmationThreshold = 2
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = reconcileCatalogTx(tx, platform, current, confirmationThreshold, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Catalog(platform)
}

func (s *Store) ReconcileCatalogs(catalogs map[string][]CatalogItem,
	confirmationThreshold int,
) (map[string][]CatalogItem, error) {
	if len(catalogs) == 0 {
		return nil, errors.New("no catalogs supplied")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, platform := range []string{"wii", "gamecube"} {
		current, ok := catalogs[platform]
		if !ok {
			continue
		}
		if err = reconcileCatalogTx(
			tx, platform, current, confirmationThreshold, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	result := make(map[string][]CatalogItem, len(catalogs))
	for platform := range catalogs {
		result[platform], err = s.Catalog(platform)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func reconcileCatalogTx(tx *sql.Tx, platform string, current []CatalogItem,
	confirmationThreshold int, now string,
) error {
	if platform != "wii" && platform != "gamecube" {
		return errors.New("invalid catalog platform")
	}
	if confirmationThreshold < 1 {
		confirmationThreshold = 2
	}
	upsert, err := tx.Prepare(`INSERT INTO catalog_items(
 platform,item_id,payload_json,availability,missing_observations,last_seen_utc,missing_confirmed_utc)
 VALUES(?,?,?,'playable',0,?,NULL)
 ON CONFLICT(platform,item_id) DO UPDATE SET payload_json=excluded.payload_json,
 availability='playable',missing_observations=0,last_seen_utc=excluded.last_seen_utc,
 missing_confirmed_utc=NULL`)
	if err != nil {
		return err
	}
	defer upsert.Close()
	seen := make(map[string]struct{}, len(current))
	for _, item := range current {
		if item.ID == "" || !json.Valid(item.Payload) {
			return errors.New("invalid catalog item")
		}
		seen[item.ID] = struct{}{}
		if _, err = upsert.Exec(platform, item.ID, []byte(item.Payload), now); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT item_id,missing_observations FROM catalog_items WHERE platform=?`,
		platform)
	if err != nil {
		return err
	}
	var missing []struct {
		id    string
		count int
	}
	for rows.Next() {
		var item struct {
			id    string
			count int
		}
		if err = rows.Scan(&item.id, &item.count); err != nil {
			rows.Close()
			return err
		}
		if _, ok := seen[item.id]; !ok {
			missing = append(missing, item)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	markMissing, err := tx.Prepare(`UPDATE catalog_items SET availability=?,
 missing_observations=?,missing_confirmed_utc=COALESCE(missing_confirmed_utc,?)
 WHERE platform=? AND item_id=?`)
	if err != nil {
		return err
	}
	defer markMissing.Close()
	for _, item := range missing {
		count := item.count + 1
		availability := sourcehealth.AvailabilityValidationRequired
		var confirmed any
		if count >= confirmationThreshold {
			availability = sourcehealth.AvailabilityMissingConfirmed
			confirmed = now
		}
		if _, err = markMissing.Exec(availability, count, confirmed, platform, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Catalog(platform string) ([]CatalogItem, error) {
	return readCatalog(s.db, platform)
}

type sqlQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func readCatalog(query sqlQuerier, platform string) ([]CatalogItem, error) {
	rows, err := query.Query(`SELECT item_id,payload_json,availability,
 missing_observations,last_seen_utc,missing_confirmed_utc
 FROM catalog_items WHERE platform=? ORDER BY item_id`, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CatalogItem
	for rows.Next() {
		var item CatalogItem
		var lastSeen string
		var confirmed sql.NullString
		item.Platform = platform
		if err = rows.Scan(&item.ID, &item.Payload, &item.Availability,
			&item.MissingObservations, &lastSeen, &confirmed); err != nil {
			return nil, err
		}
		item.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		if confirmed.Valid {
			item.MissingConfirmed, _ = time.Parse(time.RFC3339Nano, confirmed.String)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AcknowledgeMissing(platform, id string) error {
	result, err := s.db.Exec(`DELETE FROM catalog_items
 WHERE platform=? AND item_id=? AND availability='missing-confirmed'`, platform, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return errors.New("catalog item is not confirmed missing")
	}
	return nil
}

func (s *Store) RecordSourceEvent(sourceID, code, message string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO source_events(source_id,code,message,created_utc)
 VALUES(?,?,?,?)`, sourceID, code, bounded(message, 240),
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM source_events WHERE id IN (
 SELECT id FROM source_events ORDER BY id DESC LIMIT -1 OFFSET 1000)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordAuditEvent(eventType, detail string) error {
	if eventType == "" || len(eventType) > 64 {
		return errors.New("invalid audit event")
	}
	_, err := s.db.Exec(`INSERT INTO audit_events(event_type,detail,created_utc)
 VALUES(?,?,?)`, eventType, bounded(detail, 240),
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) SaveCompatibility(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO compatibility_cache(singleton,result_json,checked_utc)
 VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET
 result_json=excluded.result_json,checked_utc=excluded.checked_utc`, data,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) LoadCompatibility(target any) error {
	var data []byte
	if err := s.db.QueryRow(`SELECT result_json FROM compatibility_cache WHERE singleton=1`).
		Scan(&data); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (s *Store) SavePerformanceSession(summary perf.SessionSummary, maximum, retentionDays int) error {
	if summary.ID == "" || summary.End.IsZero() {
		return errors.New("incomplete performance session")
	}
	if maximum < 1 {
		maximum = 100
	}
	if retentionDays < 1 || retentionDays > 3650 {
		retentionDays = 30
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR REPLACE INTO performance_sessions(
 session_id,start_utc,end_utc,summary_json) VALUES(?,?,?,?)`,
		summary.ID, summary.Start.UTC().Format(time.RFC3339Nano),
		summary.End.UTC().Format(time.RFC3339Nano), data); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM performance_sessions WHERE session_id IN (
 SELECT session_id FROM performance_sessions ORDER BY end_utc DESC LIMIT -1 OFFSET ?)`,
		maximum); err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
	if _, err = tx.Exec(`DELETE FROM performance_sessions WHERE end_utc < ?`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PerformanceSessions(limit int) ([]perf.SessionSummary, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT summary_json FROM performance_sessions
 ORDER BY end_utc DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []perf.SessionSummary
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var summary perf.SessionSummary
		if err = json.Unmarshal(data, &summary); err != nil {
			return nil, fmt.Errorf("decode performance session: %w", err)
		}
		result = append(result, summary)
	}
	return result, rows.Err()
}

func bounded(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// migrateSourceIdentity is additive. Existing catalogs, receipts and source trust
// stay intact; preflight enrolls a stable ID only after legacy checks succeed.
func (s *Store) migrateSourceIdentity() error {
	var applied int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=3`).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}
	if s.preexisting {
		if _, err := s.db.Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
			return err
		}
		if err := backupDatabase(s.path, ".pre-schema3.bak"); err != nil {
			return fmt.Errorf("back up source identity migration: %w", err)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`ALTER TABLE source_roots ADD COLUMN filesystem_id TEXT NOT NULL DEFAULT '';
 ALTER TABLE source_roots ADD COLUMN root_inode INTEGER NOT NULL DEFAULT 0;
 INSERT INTO schema_migrations(version,applied_utc) VALUES(3,strftime('%Y-%m-%dT%H:%M:%fZ','now'));`); err != nil {
		return err
	}
	return tx.Commit()
}
