package sourceidentity

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestFilesystemIDsRequireKnownPersistentSemantics(t *testing.T) {
	for _, tc := range []struct {
		kind int64
		want string
	}{
		{0x2fc12fc1, "zfs:12345678fedcba98"},
		{0xef53, "ext4:12345678fedcba98"},
		{0x01021994, ""}, // tmpfs
		{0x794c7630, ""}, // overlay
		{0x6969, ""},     // NFS
	} {
		stat := unix.Statfs_t{Type: tc.kind, Fsid: unix.Fsid{Val: [2]int32{0x12345678, -19088744}}}
		if got := filesystemID(stat); got != tc.want {
			t.Fatalf("type=%x got=%q want=%q", tc.kind, got, tc.want)
		}
		stat.Fsid = unix.Fsid{}
		if filesystemID(stat) != "" {
			t.Fatal("zero filesystem identity trusted")
		}
	}
}

func TestRenumberingRequiresTheEnrolledFilesystem(t *testing.T) {
	if !SameFilesystem("zfs:dataset-a", 146, "zfs:dataset-a", 66) {
		t.Fatal("same dataset rejected after renumbering")
	}
	for _, id := range []string{"", "zfs:dataset-b"} {
		if SameFilesystem("zfs:dataset-a", 146, id, 146) {
			t.Fatal("recycled device number bypassed filesystem identity")
		}
	}
	if SameFilesystem("", 146, "zfs:dataset-a", 66) {
		t.Fatal("legacy identity was silently accepted on different device")
	}
	if !SameFilesystem("", 146, "zfs:dataset-a", 146) {
		t.Fatal("matching legacy identity cannot migrate")
	}
}
