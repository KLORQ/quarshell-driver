//go:build linux

package isolation

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestBaselineSeccompProgramShape(t *testing.T) {
	program := baselineSeccompProgram()
	if len(program) != len(baselineDeniedSyscalls)+3 {
		t.Fatalf("program length = %d", len(program))
	}
	if program[0].Code != unix.BPF_LD|unix.BPF_W|unix.BPF_ABS || program[0].K != 0 {
		t.Fatalf("first instruction = %+v", program[0])
	}
	allow := program[len(program)-2]
	deny := program[len(program)-1]
	if allow.Code != unix.BPF_RET|unix.BPF_K || allow.K != unix.SECCOMP_RET_ALLOW {
		t.Fatalf("allow instruction = %+v", allow)
	}
	if deny.K != unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM) {
		t.Fatalf("deny instruction = %+v", deny)
	}
	for index := 0; index < len(baselineDeniedSyscalls); index++ {
		instruction := program[index+1]
		if instruction.K != uint32(baselineDeniedSyscalls[index]) {
			t.Fatalf("instruction %d = %+v", index, instruction)
		}
		if int(instruction.Jt) > len(program)-1 {
			t.Fatalf("jump offset out of range: %d", instruction.Jt)
		}
	}
}
