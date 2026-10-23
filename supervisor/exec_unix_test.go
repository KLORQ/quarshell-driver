//go:build unix

package supervisor

import (
	"context"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"
)

func readPTY(t *testing.T, session ExecSession) string {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		var builder strings.Builder
		buffer := make([]byte, 4096)
		for {
			read, err := session.Read(buffer)
			if read > 0 {
				builder.Write(buffer[:read])
			}
			if err != nil {
				result <- builder.String()
				return
			}
		}
	}()
	select {
	case output := <-result:
		return output
	case <-time.After(2 * time.Second):
		_ = session.Close()
		t.Fatalf("timed out reading pty")
		return ""
	}
}

func TestStartExecReadsOutputAndWaits(t *testing.T) {
	session, err := StartExec(context.Background(), ExecRequest{Command: []string{"/bin/sh", "-c", "printf hello"}})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	output := readPTY(t, session)
	if !strings.Contains(output, "hello") {
		t.Fatalf("output = %q", output)
	}
	status, err := session.Wait()
	if err != nil || !status.Exited || status.Code != 0 {
		t.Fatalf("status = %+v err=%v", status, err)
	}
	_ = session.Close()
}

func TestStartExecWriteAndSignal(t *testing.T) {
	session, err := StartExec(context.Background(), ExecRequest{Command: []string{"/bin/cat"}})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	if _, err := session.Write([]byte("ping\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	output := make(chan string, 1)
	go func() {
		buffer := make([]byte, 4096)
		var builder strings.Builder
		for !strings.Contains(builder.String(), "ping") {
			read, err := session.Read(buffer)
			if read > 0 {
				builder.Write(buffer[:read])
			}
			if err != nil {
				break
			}
		}
		output <- builder.String()
	}()
	select {
	case text := <-output:
		if !strings.Contains(text, "ping") {
			t.Fatalf("output = %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for echo")
	}
	if err := session.Signal("TERM"); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	status, err := session.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if status.Signal != int(syscall.SIGTERM) && status.Code == 0 {
		t.Fatalf("status = %+v", status)
	}
	_ = session.Close()
}

func TestStartExecRejectsInvalidRequest(t *testing.T) {
	cases := map[string]ExecRequest{
		"empty":        {},
		"nul command":  {Command: []string{"a\x00b"}},
		"relative dir": {Command: []string{"/bin/true"}, Workdir: "tmp"},
		"bad env":      {Command: []string{"/bin/true"}, Environment: map[string]string{"A=B": "x"}},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := StartExec(context.Background(), request); err == nil {
				t.Fatalf("invalid request accepted")
			}
		})
	}
}

func TestStartExecHandlesIO(t *testing.T) {
	session, err := StartExec(context.Background(), ExecRequest{Command: []string{"/bin/sh", "-c", "cat"}})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	if _, err := io.WriteString(session, "abc\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = session.Close()
}
