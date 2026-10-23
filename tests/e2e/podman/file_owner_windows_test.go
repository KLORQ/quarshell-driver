//go:build windows

package podman_test

import "os"

type fileOwner struct {
	uid uint64
	gid uint64
}

func hostFileOwner(os.FileInfo) (fileOwner, bool) {
	// Windows file ownership is represented by ACLs rather than Unix UID/GID.
	return fileOwner{}, false
}
