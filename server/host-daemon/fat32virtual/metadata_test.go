package fat32virtual

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestSharedFATCopiesAndExactFreeSpace(t *testing.T) {
	file := testFile(t, t.TempDir(), "disc.iso", 65537)
	layout, metadata, err := Build(8<<30, "WIIBRIDGE", "free-space", []File{file})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := Open(layout, metadata, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	g := layout.Geometry
	fatOffset := (g.PartitionStart + g.ReservedSectors) * SectorSize
	fatBytes := g.FATSectors * SectorSize
	fat := make([]byte, fatBytes)
	copy2 := make([]byte, fatBytes)
	if _, err = backend.ReadAt(fat, fatOffset); err != nil {
		t.Fatal(err)
	}
	if _, err = backend.ReadAt(copy2, fatOffset+fatBytes); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fat, copy2) {
		t.Fatal("virtual FAT copies differ")
	}
	var stored []int64
	for _, extent := range layout.MetadataExtents {
		if extent.VirtualOffset == fatOffset || extent.VirtualOffset == fatOffset+fatBytes {
			stored = append(stored, extent.StorageOffset)
		}
	}
	if len(stored) != 2 || stored[0] != stored[1] {
		t.Fatal("immutable FAT copies were stored twice")
	}
	clusters := (g.PartitionSectors - g.FirstDataSector) / g.SectorsPerCluster
	var free uint32
	firstFree := uint32(0xffffffff)
	for cluster := int64(2); cluster < clusters+2; cluster++ {
		if binary.LittleEndian.Uint32(fat[cluster*4:])&0x0fffffff == 0 {
			free++
			if firstFree == 0xffffffff {
				firstFree = uint32(cluster)
			}
		}
	}
	for _, sector := range []int64{1, 7} {
		info := make([]byte, SectorSize)
		if _, err = backend.ReadAt(info, (g.PartitionStart+sector)*SectorSize); err != nil {
			t.Fatal(err)
		}
		if binary.LittleEndian.Uint32(info[488:]) != free ||
			binary.LittleEndian.Uint32(info[492:]) != firstFree {
			t.Fatalf("FSInfo copy %d differs from independent FAT count", sector)
		}
	}
	// Callers cannot mutate an open backend through their input metadata.
	clear(metadata)
	again := make([]byte, fatBytes)
	if _, err = backend.ReadAt(again, fatOffset); err != nil || !bytes.Equal(again, fat) {
		t.Fatalf("backend lost its defensive metadata copy: %v", err)
	}
	if err = Validate(layout, metadata); err == nil {
		t.Fatal("tampered stored metadata accepted by validation")
	}
	if _, err = Open(layout, metadata, 1); err == nil {
		t.Fatal("tampered stored metadata accepted on next open")
	}
}
