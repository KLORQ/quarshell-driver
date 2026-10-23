package mounts

import "testing"

func TestValidateContainerMountTarget(t *testing.T) {
	cases := []struct {
		target string
		ok     bool
	}{
		{"/workspace/src", true},
		{"/data", true},
		{"/quarshell", false},
		{"/quarshell/data", false},
		{"/quarshell/policy.yaml", false},
		{"/proc", false},
		{"/sys/fs", false},
		{"/dev/null", false},
		{"/run/quarshell/ssh.sock", false},
		{"/run", false},
		{"/etc/quarshell", false},
		{"/var/run/quarshell", false},
		{"relative", false},
		{"/workspace/../quarshell", false}, // has ..
	}
	for _, tc := range cases {
		err := ValidateContainerMountTarget(tc.target)
		if tc.ok && err != nil {
			t.Fatalf("%s: unexpected err %v", tc.target, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: expected error", tc.target)
		}
	}
}

func TestValidateUploadDest(t *testing.T) {
	if err := ValidateUploadDest("/workspace/file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUploadDest("/workspace"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUploadDest("/quarshell/data/x"); err == nil {
		t.Fatal("expected refuse /quarshell")
	}
}

func TestPathsOverlap(t *testing.T) {
	if !PathsOverlap("/quarshell", "/quarshell/data") {
		t.Fatal("expected overlap")
	}
	if PathsOverlap("/workspace", "/quarshell") {
		t.Fatal("no overlap")
	}
}
