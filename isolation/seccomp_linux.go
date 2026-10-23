//go:build linux

package isolation

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// baselineDeniedSyscalls are privileged operations that a sandboxed workload
// must never perform. The filter returns EPERM; it is a deny-list, not the
// final seccomp notification broker.
var baselineDeniedSyscalls = []uintptr{
	unix.SYS_MOUNT,
	unix.SYS_UMOUNT2,
	unix.SYS_PIVOT_ROOT,
	unix.SYS_PTRACE,
	unix.SYS_KEXEC_LOAD,
	unix.SYS_KEXEC_FILE_LOAD,
	unix.SYS_REBOOT,
	unix.SYS_BPF,
	unix.SYS_PERF_EVENT_OPEN,
	unix.SYS_KEYCTL,
	unix.SYS_ADD_KEY,
	unix.SYS_REQUEST_KEY,
	unix.SYS_INIT_MODULE,
	unix.SYS_FINIT_MODULE,
	unix.SYS_DELETE_MODULE,
	unix.SYS_ACCT,
	unix.SYS_SWAPON,
	unix.SYS_SWAPOFF,
	unix.SYS_SETNS,
	unix.SYS_PROCESS_VM_READV,
	unix.SYS_PROCESS_VM_WRITEV,
	unix.SYS_OPEN_BY_HANDLE_AT,
	unix.SYS_NAME_TO_HANDLE_AT,
	unix.SYS_USERFAULTFD,
}

func baselineSeccompProgram() []unix.SockFilter {
	filter := make([]unix.SockFilter, 0, len(baselineDeniedSyscalls)+3)
	filter = append(filter, unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0})
	count := len(baselineDeniedSyscalls)
	for index, syscall := range baselineDeniedSyscalls {
		filter = append(filter, unix.SockFilter{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jt:   uint8(count - index),
			Jf:   0,
			K:    uint32(syscall),
		})
	}
	filter = append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	filter = append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)})
	return filter
}

func installBaselineSeccomp() error {
	filter := baselineSeccompProgram()
	if len(filter) == 0 {
		return fmt.Errorf("install seccomp filter: empty program")
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return fmt.Errorf("install seccomp filter: %w", err)
	}
	return nil
}
