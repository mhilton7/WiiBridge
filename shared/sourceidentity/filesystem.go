// Package sourceidentity distinguishes filesystem identity from a kernel's
// temporary device number. Unknown filesystems retain strict device checks.
package sourceidentity

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// FilesystemID reads metadata only. ZFS derives f_fsid from its dataset fsid
// GUID; ext4 derives it from the filesystem UUID. Do not generalize this to
// tmpfs, overlay, network filesystems, or an arbitrary nonzero f_fsid.
func FilesystemID(path string) (string, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return "", err
	}
	return filesystemID(stat), nil
}

func filesystemID(stat unix.Statfs_t) string {
	name := ""
	switch stat.Type {
	case 0x2fc12fc1:
		name = "zfs"
	case 0xef53:
		name = "ext4"
	default:
		return ""
	}
	if stat.Fsid.Val == [2]int32{} {
		return ""
	}
	return fmt.Sprintf("%s:%08x%08x", name, uint32(stat.Fsid.Val[0]), uint32(stat.Fsid.Val[1]))
}

// SameFilesystem never falls back to a device number after a persistent ID
// has been enrolled. A disappeared or different filesystem must fail closed.
func SameFilesystem(expectedID string, expectedDevice uint64, actualID string, actualDevice uint64) bool {
	if expectedID != "" {
		return actualID == expectedID
	}
	return expectedDevice == actualDevice
}
