//go:build !windows

package docker

import "os"

func supervisorBinaryExecutable(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
