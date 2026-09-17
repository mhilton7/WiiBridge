package sourcehealth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStableIdentitySurvivesDeviceAndMountRenumbering(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "game"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	id := "zfs:1234567890abcdef"
	probe := func(string) (string, error) { return id, nil }
	first, err := preflight(root, nil, probe)
	if err != nil {
		t.Fatal(err)
	}
	previous := Successful(first.Record, 1)
	previous.LastKnownDevice++
	previous.LastKnownMountInfo = "0:146:/games:/library"
	recovered, err := preflight(root, &previous, probe)
	if err != nil || recovered.Record.State != StateAvailable || recovered.Record.SourceID != previous.SourceID {
		t.Fatalf("remount: %#v %v", recovered, err)
	}
	for _, current := range []string{"zfs:different", ""} {
		id = current
		blocked, blockErr := preflight(root, &previous, probe)
		if blockErr == nil || blocked.Record.State != StateChanged || blocked.Record.FilesystemID != previous.FilesystemID || blocked.Record.LastSuccessfulItemCount != 1 {
			t.Fatalf("replacement accepted: %#v %v", blocked, blockErr)
		}
	}
	id = previous.FilesystemID
	if err = os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root); _ = os.Rename(root+"-original", root) })
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "game"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = preflight(root, &previous, probe); err == nil {
		t.Fatal("different directory on same dataset accepted")
	}
}

func TestLegacyEnrollmentRequiresOriginalMountAndReadableFilesystem(t *testing.T) {
	root := t.TempDir()
	legacy, err := preflight(root, nil, func(string) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	probe := func(string) (string, error) { return "zfs:1234567890abcdef", nil }
	enrolled, err := preflight(root, &legacy.Record, probe)
	if err != nil || enrolled.Record.FilesystemID == "" || enrolled.Record.RootInode == 0 {
		t.Fatalf("enrollment: %#v %v", enrolled, err)
	}
	legacy.Record.LastKnownDevice++
	blocked, err := preflight(root, &legacy.Record, probe)
	if err == nil || blocked.Record.FilesystemID != "" {
		t.Fatal("legacy mismatch silently enrolled")
	}
	_, err = preflight(root, &enrolled.Record, func(string) (string, error) { return "", os.ErrPermission })
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unavailable metadata accepted: %v", err)
	}
}
