//go:build !windows

package docker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSupervisorBinaryRequiresExecutePermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor")
	if err := os.WriteFile(path, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if supervisorBinaryExecutable(info) {
		t.Fatal("non-executable supervisor was accepted")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !supervisorBinaryExecutable(info) {
		t.Fatal("executable supervisor was rejected")
	}
}
