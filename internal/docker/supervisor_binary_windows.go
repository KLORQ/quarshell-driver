package docker

import "os"

func supervisorBinaryExecutable(info os.FileInfo) bool {
	return info.Mode().IsRegular()
}
