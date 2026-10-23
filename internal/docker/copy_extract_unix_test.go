//go:build !windows

package docker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyExtractCapsFileModes(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		mode int64
		want os.FileMode
	}{
		{"data", 0o666, 0o644},
		{"script", 0o777, 0o755},
	} {
		if err := extractCopyTar(testCopyArchive(t, tc.name, tc.mode), root, true, "source"); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, tc.name))
		if err != nil || info.Mode().Perm() != tc.want {
			t.Fatalf("%s mode: %v, %v", tc.name, info, err)
		}
	}
}
