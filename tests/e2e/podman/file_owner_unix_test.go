//go:build !windows

package podman_test

import (
	"os"
	"syscall"
)

type fileOwner struct {
	uid uint64
	gid uint64
}

func hostFileOwner(info os.FileInfo) (fileOwner, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileOwner{}, false
	}
	return fileOwner{uid: uint64(stat.Uid), gid: uint64(stat.Gid)}, true
}
