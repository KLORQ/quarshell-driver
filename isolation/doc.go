// Package isolation owns the Linux launcher, broker, identity verification, and
// syscall/FD boundary.
//
// Platform-independent admission validates and sanitizes launch requests. On
// Linux a restricted child is re-executed with Landlock, no_new_privs, a
// dropped bounding capability set, and a verified process identity. The launch
// spec crosses a socketpair and the child checks SO_PEERCRED before reading it.
// Other platforms return domain.ErrUnsupported instead of pretending isolation
// is available. Live kernel acceptance runs on Linux hosts.
//
// A supervisor binary must call RunChildIfRequested at startup (or point
// LinuxOptions.Self at cmd/quarshell-isolation-child).
package isolation
