// Package model contains the immutable catalog and extent format shared by the
// host and Pi controller.
package model

import "time"

const SectorSize = 512

type Source struct {
	Path         string `json:"path"`
	Offset       int64  `json:"offset"`
	Length       int64  `json:"length"`
	Size         int64  `json:"size"`
	ModUnix      int64  `json:"mod_unix_ns"`
	FilesystemID string `json:"filesystem_id,omitempty"`
	Device       uint64 `json:"device"`
	Inode        uint64 `json:"inode"`
}

type Game struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Sources      []Source `json:"sources"`
	Size         int64    `json:"size"`
	Availability string   `json:"availability,omitempty"`
}

type VirtualFile struct {
	Path         string `json:"path"`
	GameID       string `json:"game_id"`
	LogicalStart int64  `json:"logical_source_offset"`
	Length       int64  `json:"length"`
}

type Snapshot struct {
	SnapshotID      string        `json:"snapshot_id"`
	CatalogID       string        `json:"catalog_id"`
	VirtualDiskSize int64         `json:"virtual_disk_size"`
	MetadataHash    string        `json:"filesystem_metadata_hash"`
	Application     string        `json:"application_version"`
	Created         time.Time     `json:"creation_timestamp"`
	Games           []Game        `json:"games"`
	VirtualFiles    []VirtualFile `json:"virtual_files"`
}
