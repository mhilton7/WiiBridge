package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"wiibridge/tests/testutil"
)

func TestWiiFileChecksSurviveRenumberingButRejectReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "synthetic.wbfs")
	if err := testutil.SyntheticWBFS(path, "SWII01", "Synthetic", 2<<20); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(root)
	if err != nil || len(result.Games) != 1 {
		t.Fatalf("scan: %#v %v", result, err)
	}
	source := result.Games[0].Sources[0]
	if source.FilesystemID == "" {
		t.Skip("run on ext4 or ZFS for persistent filesystem integration")
	}
	source.Device++
	if err = VerifySource(source); err != nil {
		t.Fatal("device-only change rejected", err)
	}
	wrong := source
	wrong.FilesystemID += "different"
	if VerifySource(wrong) == nil {
		t.Fatal("wrong filesystem accepted")
	}
	legacy := source
	legacy.FilesystemID = ""
	if VerifySource(legacy) == nil {
		t.Fatal("unenrolled legacy device change accepted")
	}
	if err = os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err = testutil.SyntheticWBFS(path, "SWII01", "Synthetic", 2<<20); err != nil {
		t.Fatal(err)
	}
	if VerifySource(source) == nil {
		t.Fatal("different file accepted")
	}
}
