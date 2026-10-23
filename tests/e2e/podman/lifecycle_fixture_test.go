package podman_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-core/resourceadmission"
	"github.com/KLORQ/quarshell-driver/driver"
	podman "github.com/KLORQ/quarshell-driver/podman"
	"github.com/KLORQ/quarshell-driver/tests/internal/testenv"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

func TestPodmanCanonicalMainProcessTTYLifecycle(t *testing.T) {
	ctx := testenv.RequireContainers(t)
	_, endpoint, workspace := testenv.RunPodmanDaemon(ctx, t)
	t.Setenv("DOCKER_HOST", endpoint)
	engine, err := podman.NewWithConfig(podman.Config{
		ImagePullPolicy:         "missing",
		NetworkName:             "quarshell-podman-main-tty",
		HealthCheckIntervalSecs: 0,
		UsernsMode:              "host",
	})
	if err != nil {
		t.Fatalf("create Podman driver: %v", err)
	}
	defer engine.Close()
	name := "testcontainers-podman-main-tty"
	handle, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
		Name: name, Image: testenv.AlpineImage, Workspace: workspace,
		Command: []string{"sh", "-c", "read value; sleep 1; printf 'QUARSHELL_PODMAN_TTY_REPLY:%s\\n' \"$value\"; exit 7"},
		TTY:     true, NoHarden: true,
	}))
	if err != nil {
		t.Fatalf("create Podman TTY sandbox: %v", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := engine.Delete(cleanupCtx, handle.ID); err != nil {
			t.Errorf("delete Podman TTY sandbox: %v", err)
		}
	}()
	if err := engine.Start(ctx, handle.ID); err != nil {
		t.Fatalf("start Podman TTY sandbox: %v", err)
	}
	session, err := engine.OpenMainProcessTTY(ctx, handle.ID, 80, 24)
	if err != nil {
		t.Fatalf("attach Podman canonical TTY: %v", err)
	}
	outputDone := make(chan error, 1)
	var output bytes.Buffer
	go func() {
		_, err := io.Copy(&output, session)
		outputDone <- err
	}()
	if _, err := io.WriteString(session, "quarshell-podman-input\n"); err != nil {
		_ = session.Close()
		t.Fatalf("write Podman TTY input: %v", err)
	}
	if err := engine.ResizeMainProcessTTY(ctx, handle.ID, 100, 32); err != nil {
		_ = session.Close()
		t.Fatalf("resize Podman canonical TTY: %v", err)
	}
	code, waitErr := engine.WaitMainProcess(ctx, handle.ID)
	_ = session.Close()
	if outputErr := <-outputDone; outputErr != nil && !errors.Is(outputErr, io.EOF) && !errors.Is(outputErr, net.ErrClosed) {
		t.Fatalf("read Podman TTY output: %v", outputErr)
	}
	if waitErr != nil || code != 7 {
		t.Fatalf("Podman TTY main-process exit=%d err=%v, want exit 7", code, waitErr)
	}
	if !strings.Contains(output.String(), "QUARSHELL_PODMAN_TTY_REPLY:quarshell-podman-input") {
		t.Fatalf("Podman TTY output=%q; main-process reply missing", output.String())
	}
}

func TestPodmanDriverLifecycleAgainstTestcontainersEngine(t *testing.T) {
	ctx := testenv.RequireContainers(t)
	_, endpoint, workspace := testenv.RunPodmanDaemon(ctx, t)
	t.Setenv("DOCKER_HOST", endpoint)
	engineClient, err := client.New(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create Podman API client: %v", err)
	}
	defer engineClient.Close()
	for _, volume := range []string{"quarshell-testcontainers-podman-data", "quarshell-testcontainers-podman-readonly"} {
		if _, err := engineClient.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volume}); err != nil {
			t.Fatalf("create caller volume fixture %q: %v", volume, err)
		}
		defer func() {
			_, _ = engineClient.VolumeRemove(context.Background(), volume, client.VolumeRemoveOptions{Force: true})
		}()
	}

	engine, err := podman.NewWithConfig(podman.Config{
		ImagePullPolicy:         "missing",
		NetworkName:             "quarshell-testcontainers",
		HealthCheckIntervalSecs: 0,
		UsernsMode:              "host",
		AllowDriverConfig:       true,
		ResourceAdmission:       &resourceadmission.Config{Enabled: false},
	})
	if err != nil {
		t.Fatalf("create Podman driver: %v", err)
	}
	defer engine.Close()

	handle, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
		Name:             "testcontainers-podman-lifecycle",
		Image:            testenv.AlpineImage,
		Workspace:        workspace,
		Command:          []string{"sh", "-c", "echo ready; sleep 60"},
		NoHarden:         true,
		CPU:              0.25,
		MemoryBytes:      128 * 1024 * 1024,
		PidsLimit:        64,
		DriverConfigJSON: `{"mounts":[{"type":"volume","source":"quarshell-testcontainers-podman-data","target":"/persist","read_only":false},{"type":"volume","source":"quarshell-testcontainers-podman-readonly","target":"/readonly","read_only":true},{"type":"tmpfs","target":"/scratch","size_bytes":4096,"read_only":false}]}`,
	}))
	if err != nil {
		t.Fatalf("create sandbox through Podman driver: %v", err)
	}
	info, err := engine.Inspect(ctx, string(handle.ID))
	if err != nil {
		t.Fatalf("inspect Podman sandbox: %v", err)
	}
	if info.ID == "" || info.Name == "" {
		t.Fatalf("Podman inspect returned incomplete info: %+v", info)
	}
	defer func() { _ = engine.Delete(context.Background(), handle.ID) }()
	if err := engine.Start(ctx, handle.ID); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	uploadSource := filepath.Join(t.TempDir(), "upload-fixture")
	if err := os.Mkdir(uploadSource, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploadSource, "marker"), []byte("uploaded"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("marker", filepath.Join(uploadSource, "link")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := os.Symlink("missing", filepath.Join(uploadSource, "dangling")); err != nil {
		t.Skipf("dangling symlink creation unavailable: %v", err)
	}
	if err := engine.CopyTo(ctx, handle.ID, uploadSource, "/workspace"); err != nil {
		t.Fatalf("copy upload fixture into Podman sandbox: %v", err)
	}
	uploadTarget := "/workspace/" + filepath.Base(uploadSource)
	result, err := engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "test \"$(id -u)\" = 0 && printf volume > /persist/marker && test -w /scratch && ! touch /readonly/marker"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("exec in Podman sandbox: result=%+v err=%v", result, err)
	}
	result, err = engine.Exec(ctx, handle.ID, driver.ExecRequest{
		Argv:        []string{"sh", "-c", "cat > /workspace/stdin-fixture"},
		StdinReader: strings.NewReader("stdin-streamed-without-tty\n"),
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("exec piped stdin: result=%+v err=%v", result, err)
	}
	result, err = engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "test \"$(cat /workspace/stdin-fixture)\" = stdin-streamed-without-tty"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("piped stdin was not delivered to the process: result=%+v err=%v", result, err)
	}
	result, err = engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "test \"$(cat " + uploadTarget + "/marker)\" = uploaded && test -L " + uploadTarget + "/link && test \"$(readlink " + uploadTarget + "/link)\" = marker && test -L " + uploadTarget + "/dangling"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("uploaded files or symlinks were not preserved: result=%+v err=%v", result, err)
	}
	downloadTarget := filepath.Join(t.TempDir(), "downloaded-upload")
	if err := engine.CopyFrom(ctx, handle.ID, uploadTarget, downloadTarget); err != nil {
		t.Fatalf("copy uploaded directory back from Podman sandbox: %v", err)
	}
	downloadedUpload := filepath.Join(downloadTarget, filepath.Base(uploadSource))
	if got, err := os.ReadFile(filepath.Join(downloadedUpload, "marker")); err != nil || string(got) != "uploaded" {
		t.Fatalf("downloaded file=%q err=%v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(downloadedUpload, "link")); err != nil || got != "marker" {
		t.Fatalf("downloaded symlink=%q err=%v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(downloadedUpload, "dangling")); err != nil || got != "missing" {
		t.Fatalf("downloaded dangling symlink=%q err=%v", got, err)
	}
	fileInfo, err := os.Stat(filepath.Join(downloadedUpload, "marker"))
	if err != nil {
		t.Fatalf("stat downloaded file: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o644 {
		t.Fatalf("downloaded file mode=%v", fileInfo.Mode().Perm())
	}
	if err := engine.Stop(ctx, handle.ID); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}
	if err := engine.Start(ctx, handle.ID); err != nil {
		t.Fatalf("restart sandbox: %v", err)
	}
	result, err = engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "test \"$(cat /persist/marker)\" = volume"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("named volume did not persist across restart: result=%+v err=%v", result, err)
	}
	if err := engine.Delete(ctx, handle.ID); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
}

func TestPodmanDriverRecoversAfterServiceRestart(t *testing.T) {
	ctx := testenv.RequireContainers(t)
	daemon, endpoint, workspace := testenv.RunPodmanDaemon(ctx, t)
	t.Setenv("DOCKER_HOST", endpoint)

	api, err := client.New(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create Podman API client: %v", err)
	}
	defer api.Close()
	engine, err := podman.NewWithConfig(podman.Config{
		ImagePullPolicy:         "missing",
		NetworkName:             "quarshell-testcontainers-recovery",
		HealthCheckIntervalSecs: 0,
		UsernsMode:              "host",
	})
	if err != nil {
		t.Fatalf("create Podman driver: %v", err)
	}
	defer engine.Close()

	handle, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
		Name:      "testcontainers-podman-recovery",
		Image:     testenv.AlpineImage,
		Workspace: workspace,
		Command:   []string{"sh", "-c", "sleep 120"},
		NoHarden:  true,
	}))
	if err != nil {
		t.Fatalf("create recovery sandbox: %v", err)
	}
	defer func() { _ = engine.Delete(context.Background(), handle.ID) }()
	if err := engine.Start(ctx, handle.ID); err != nil {
		t.Fatalf("start recovery sandbox: %v", err)
	}
	result, err := engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "printf podman-recovery > /workspace/recovery-marker"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("write recovery marker: result=%+v err=%v", result, err)
	}

	if _, _, err := daemon.Exec(ctx, []string{"sh", "-c", "kill \"$(cat /tmp/podman-service.pid)\""}); err != nil {
		t.Fatalf("stop Podman API service: %v", err)
	}
	outageCtx, outageCancel := context.WithTimeout(context.Background(), 5*time.Second)
	var outageErr error
	for outageCtx.Err() == nil {
		_, outageErr = api.Info(outageCtx, client.InfoOptions{})
		if outageErr != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	outageCancel()
	if outageErr == nil {
		t.Fatal("Podman API remained reachable after service stop")
	}

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readyCancel()
	var infoErr error
	for readyCtx.Err() == nil {
		_, infoErr = api.Info(readyCtx, client.InfoOptions{})
		if infoErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if infoErr != nil {
		t.Fatalf("Podman API did not recover after service restart: %v", infoErr)
	}
	result, err = engine.Exec(readyCtx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "test \"$(cat /workspace/recovery-marker)\" = podman-recovery"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("recovery sandbox state was not restored: result=%+v err=%v", result, err)
	}
}

func TestPodmanRootlessServiceRecovery(t *testing.T) {
	if os.Getenv("QUARSHELL_PODMAN_ROOTLESS") != "1" {
		t.Skip("rootless Podman service lane is not enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	workspace := strings.TrimSpace(os.Getenv("QUARSHELL_PODMAN_WORKSPACE"))
	image := strings.TrimSpace(os.Getenv("QUARSHELL_E2E_SANDBOX_IMAGE"))
	if workspace == "" || image == "" {
		t.Fatal("QUARSHELL_PODMAN_WORKSPACE and QUARSHELL_E2E_SANDBOX_IMAGE are required")
	}

	api, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("create rootless Podman API client: %v", err)
	}
	defer api.Close()
	engine, err := podman.NewWithConfig(podman.Config{
		ImagePullPolicy:         "missing",
		NetworkName:             "quarshell-rootless-recovery",
		HealthCheckIntervalSecs: 0,
		UsernsMode:              "host",
	})
	if err != nil {
		t.Fatalf("create rootless Podman driver: %v", err)
	}
	defer engine.Close()
	handle, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
		Name: "rootless-podman-service-recovery", Image: image, Workspace: workspace,
		Command: []string{"sh", "-c", "sleep 120"}, NoHarden: true,
	}))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "host-gateway isolation requires podman 6.0") {
			t.Skip("host-gateway isolation requires Podman 6.0 or newer")
		}
		t.Fatalf("create rootless recovery sandbox: %v", err)
	}
	defer func() { _ = engine.Delete(context.Background(), handle.ID) }()
	if err := engine.Start(ctx, handle.ID); err != nil {
		t.Fatalf("start rootless recovery sandbox: %v", err)
	}
	result, err := engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "printf rootless-recovery > /workspace/recovery-marker"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("write rootless recovery marker: result=%+v err=%v", result, err)
	}

	if err := osexec.CommandContext(ctx, "sh", "-lc", "kill \"$(cat /tmp/podman-rootless-service.pid)\"").Run(); err != nil {
		t.Fatalf("stop rootless Podman service: %v", err)
	}
	outageCtx, outageCancel := context.WithTimeout(ctx, 5*time.Second)
	var outageErr error
	for outageCtx.Err() == nil {
		_, outageErr = api.Info(outageCtx, client.InfoOptions{})
		if outageErr != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	outageCancel()
	if outageErr == nil {
		t.Fatal("rootless Podman API remained reachable after service stop")
	}

	readyCtx, readyCancel := context.WithTimeout(ctx, 30*time.Second)
	defer readyCancel()
	var infoErr error
	for readyCtx.Err() == nil {
		_, infoErr = api.Info(readyCtx, client.InfoOptions{})
		if infoErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if infoErr != nil {
		t.Fatalf("rootless Podman API did not recover: %v", infoErr)
	}
	result, err = engine.Exec(readyCtx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "test \"$(cat /workspace/recovery-marker)\" = rootless-recovery"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("rootless recovery sandbox state was not restored: result=%+v err=%v", result, err)
	}
}

func TestPodmanRootlessDriverLifecycle(t *testing.T) {
	if os.Getenv("QUARSHELL_PODMAN_ROOTLESS") != "1" {
		t.Skip("rootless Podman service lane is not enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	workspace := strings.TrimSpace(os.Getenv("QUARSHELL_PODMAN_WORKSPACE"))
	image := strings.TrimSpace(os.Getenv("QUARSHELL_E2E_SANDBOX_IMAGE"))
	if workspace == "" || image == "" {
		t.Fatal("QUARSHELL_PODMAN_WORKSPACE and QUARSHELL_E2E_SANDBOX_IMAGE are required")
	}
	engine, err := podman.NewWithConfig(podman.Config{
		ImagePullPolicy:         "missing",
		NetworkName:             "quarshell-rootless-lifecycle",
		HealthCheckIntervalSecs: 0,
		UsernsMode:              "keep-id",
	})
	if err != nil {
		t.Fatalf("create rootless Podman driver: %v", err)
	}
	defer engine.Close()
	const name = "rootless-podman-driver-lifecycle"
	handle, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
		Name: name, Image: image, Workspace: workspace,
		Command:  []string{"sh", "-c", "echo rootless-lifecycle-ready; exec sleep 120"},
		NoHarden: true, PersistVolume: true,
	}))
	if err != nil {
		t.Fatalf("create rootless Podman sandbox: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := engine.Delete(cleanupCtx, handle.ID); err != nil {
			t.Errorf("delete rootless Podman sandbox during cleanup: %v", err)
		}
	}()
	created, err := engine.Inspect(ctx, string(handle.ID))
	if err != nil || created.Status == "" || created.ID == "" {
		t.Fatalf("inspect created rootless sandbox: info=%+v err=%v", created, err)
	}
	if err := engine.Start(ctx, handle.ID); err != nil {
		t.Fatalf("start rootless Podman sandbox: %v", err)
	}
	marker := "/workspace/rootless-lifecycle-marker"
	result, err := engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "printf rootless-lifecycle > " + marker}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("write rootless workspace marker: result=%+v err=%v", result, err)
	}
	markerInfo, err := os.Stat(filepath.Join(workspace, "rootless-lifecycle-marker"))
	if err != nil {
		t.Fatalf("stat host-side workspace marker: %v", err)
	}
	uidField := reflect.Indirect(reflect.ValueOf(markerInfo.Sys())).FieldByName("Uid")
	if !uidField.IsValid() || uidField.Uint() != uint64(os.Getuid()) {
		t.Fatalf("workspace marker owner uid=%v, want rootless service uid %d", markerInfo.Sys(), os.Getuid())
	}

	uploadSource := filepath.Join(t.TempDir(), "rootless-upload")
	if err := os.Mkdir(uploadSource, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploadSource, "file"), []byte("rootless-copy"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(uploadSource, "link")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := engine.CopyTo(ctx, handle.ID, uploadSource, "/workspace"); err != nil {
		t.Fatalf("copy fixture into rootless sandbox: %v", err)
	}
	copyTarget := "/workspace/" + filepath.Base(uploadSource)
	result, err = engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", `test "$(cat "$1")" = rootless-copy && test -L "$2" && test "$(readlink "$2")" = file`, "sh", copyTarget + "/file", copyTarget + "/link"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("verify rootless uploaded file and symlink: result=%+v err=%v", result, err)
	}
	downloadTarget := filepath.Join(t.TempDir(), "rootless-download")
	if err := engine.CopyFrom(ctx, handle.ID, copyTarget, downloadTarget); err != nil {
		t.Fatalf("copy fixture from rootless sandbox: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(downloadTarget, filepath.Base(uploadSource), "file")); err != nil || string(got) != "rootless-copy" {
		t.Fatalf("downloaded rootless file=%q err=%v", got, err)
	}
	if err := engine.Stop(ctx, handle.ID); err != nil {
		t.Fatalf("stop rootless Podman sandbox: %v", err)
	}
	if err := engine.Start(ctx, handle.ID); err != nil {
		t.Fatalf("restart rootless Podman sandbox: %v", err)
	}
	result, err = engine.Exec(ctx, handle.ID, driver.ExecRequest{Argv: []string{"sh", "-c", "test \"$(cat " + marker + ")\" = rootless-lifecycle"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("verify rootless workspace after restart: result=%+v err=%v", result, err)
	}
	var logs bytes.Buffer
	if err := engine.Logs(ctx, handle.ID, false, &logs); err != nil || !strings.Contains(logs.String(), "rootless-lifecycle-ready") {
		t.Fatalf("rootless main-process logs=%q err=%v", logs.String(), err)
	}
	if err := engine.Delete(ctx, handle.ID); err != nil {
		t.Fatalf("delete rootless Podman sandbox: %v", err)
	}
	if _, err := engine.Inspect(ctx, string(handle.ID)); err == nil {
		t.Fatal("deleted rootless Podman sandbox is still present")
	}
}

func TestPodmanMainProcessRestartPolicyLifecycle(t *testing.T) {
	ctx := testenv.RequireContainers(t)
	daemon, endpoint, workspace := testenv.RunPodmanDaemon(ctx, t)
	supervisorBin, initBin := testenv.InstallSupervisorHelpers(ctx, t, daemon)
	policyPath := testenv.InstallSupervisorTestPolicy(ctx, t, daemon)
	image := testenv.BuildSandboxImage(ctx, t, daemon, []string{"podman", "--url", "tcp://127.0.0.1:18500"})
	t.Setenv("DOCKER_HOST", endpoint)
	api, err := client.New(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create Podman API client: %v", err)
	}
	defer api.Close()
	engine, err := podman.NewWithConfig(podman.Config{ImagePullPolicy: "missing", NetworkName: "quarshell-restart-policy", HealthCheckIntervalSecs: 0, UsernsMode: "host"})
	if err != nil {
		t.Fatalf("create Podman driver: %v", err)
	}
	defer engine.Close()
	var handles []driver.Handle
	defer func() {
		for _, handle := range handles {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = engine.Delete(cleanupCtx, handle.ID)
			cancel()
		}
	}()

	create := func(name, policy string, command []string) driver.Handle {
		t.Helper()
		handle, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
			Name: name, Image: image, Workspace: workspace, Command: command,
			InitBin: initBin, SupervisorBin: supervisorBin,
			PolicyPath: policyPath, MainProcessRestartPolicy: policy,
		}))
		if err != nil {
			t.Fatalf("create %s sandbox: %v", policy, err)
		}
		handles = append(handles, handle)
		return handle
	}
	waitForExit := func(id core.ID, want int) {
		t.Helper()
		code, err := engine.WaitMainProcess(ctx, id)
		if err != nil || code != want {
			logs, _ := api.ContainerLogs(ctx, string(id), client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
			var output []byte
			if logs != nil {
				output, _ = io.ReadAll(logs)
				_ = logs.Close()
			}
			t.Fatalf("container %s exit=%d err=%v, want %d; raw logs=%q", id, code, err, want, output)
		}
	}
	readAttempts := func(handle driver.Handle) int {
		t.Helper()
		logs, err := api.ContainerLogs(ctx, string(handle.ID), client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
		if err != nil {
			t.Fatalf("read restart lifecycle logs: %v", err)
		}
		defer logs.Close()
		data, err := io.ReadAll(logs)
		if err != nil {
			t.Fatalf("read restart lifecycle logs: %v", err)
		}
		return strings.Count(string(data), "QUARSHELL_RESTART_ATTEMPT")
	}

	never := create("restart-never", "never", []string{"sh", "-c", "echo QUARSHELL_RESTART_ATTEMPT; exit 9"})
	if err := engine.Start(ctx, never.ID); err != nil {
		t.Fatalf("start never-policy sandbox: %v", err)
	}
	waitForExit(never.ID, 9)
	if got := readAttempts(never); got != 1 {
		t.Fatalf("never policy attempts=%d, want 1", got)
	}

	onFailure := create("restart-on-failure", "on-failure", []string{"sh", "-c", "echo QUARSHELL_RESTART_ATTEMPT; exit 9"})
	if err := engine.Start(ctx, onFailure.ID); err != nil {
		t.Fatalf("start on-failure sandbox: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && readAttempts(onFailure) < 3 {
		time.Sleep(25 * time.Millisecond)
	}
	if got := readAttempts(onFailure); got < 3 {
		t.Fatalf("on-failure policy attempts=%d, want at least 3 before shutdown", got)
	}
	if err := engine.Stop(ctx, onFailure.ID); err != nil {
		t.Fatalf("stop on-failure retry loop: %v", err)
	}
	waitForExit(onFailure.ID, 9)

	always := create("restart-always", "always", []string{"sh", "-c", "echo QUARSHELL_RESTART_ATTEMPT; trap 'exit 0' TERM; sleep 60"})
	if err := engine.Start(ctx, always.ID); err != nil {
		t.Fatalf("start always-policy sandbox: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := engine.Stop(ctx, always.ID); err != nil {
		t.Fatalf("stop always-policy sandbox: %v", err)
	}
	waitForExit(always.ID, 0)
	if got := readAttempts(always); got != 1 {
		t.Fatalf("always policy restarted after external stop: attempts=%d, want 1", got)
	}
}

// TestPodmanUsernsMatrixE2E exercises user namespace modes independently of
// the proxy/network capability lane. Podman 5 cannot provide the blackhole
// route required by proxy-backed sandboxes, but that limitation must not hide
// the identity contract we can verify on the same daemon.
func TestPodmanUsernsMatrixE2E(t *testing.T) {
	if os.Getenv("QUARSHELL_PODMAN_USERNS_E2E") != "1" {
		t.Skip("set QUARSHELL_PODMAN_USERNS_E2E=1 to run the Podman userns matrix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	workspace := strings.TrimSpace(os.Getenv("QUARSHELL_PODMAN_WORKSPACE"))
	image := strings.TrimSpace(os.Getenv("QUARSHELL_E2E_SANDBOX_IMAGE"))
	endpoint := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if endpoint == "" {
		fixtureCtx := testenv.RequireContainers(t)
		_, endpoint, workspace = testenv.RunPodmanDaemon(fixtureCtx, t)
		t.Setenv("DOCKER_HOST", endpoint)
	}
	if workspace == "" || image == "" {
		t.Fatal("Podman userns matrix requires a workspace and QUARSHELL_E2E_SANDBOX_IMAGE")
	}
	engineClient, err := client.New(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create Podman Engine client: %v", err)
	}
	defer engineClient.Close()
	rootless := os.Getenv("QUARSHELL_PODMAN_ROOTLESS") == "1"
	strict := os.Getenv("QUARSHELL_PODMAN_USERNS_STRICT") == "1"
	for _, mode := range []string{"private", "auto", "host", "keep-id", "no-map"} {
		t.Run(mode, func(t *testing.T) {
			if rootless && mode == "private" {
				// Rootless Podman owns the subordinate range and rejects explicit
				// host mappings; auto is the equivalent runtime-owned case.
				if strict {
					t.Skip("expected capability skip: rootless Podman does not accept explicit private ID maps")
				}
				t.Skip("rootless Podman does not accept explicit private ID maps")
			}
			cfg := podman.Config{ImagePullPolicy: "missing", UsernsMode: mode}
			if mode == "private" {
				cfg.UIDMap = []podman.IDMapping{{ContainerID: 0, HostID: 100000, Size: 65536}}
				cfg.GIDMap = []podman.IDMapping{{ContainerID: 0, HostID: 100000, Size: 65536}}
			}
			engine, err := podman.NewWithConfig(cfg)
			if err != nil {
				t.Fatalf("create %s Podman driver: %v", mode, err)
			}
			defer engine.Close()
			name := "podman-userns-" + strings.ReplaceAll(mode, "-", "")
			handle, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
				Name: name, Image: image, Workspace: workspace,
				Command: []string{"sh", "-c", "sleep 120"}, NoHarden: true,
			}))
			if err != nil {
				if !rootless && mode == "auto" && strings.Contains(strings.ToLower(err.Error()), "not enough unused ids in user namespace") {
					t.Skip("nested rootful Podman has no unused subordinate IDs for auto userns")
				}
				if mode == "no-map" && strings.Contains(strings.ToLower(err.Error()), "nomap is only supported in rootless mode") {
					if strict && !rootless {
						t.Skip("expected capability skip: no-map is only supported in rootless mode")
					}
					if strict {
						t.Fatalf("required rootless no-map capability failed: %v", err)
					}
					t.Skip("nomap is only supported in rootless mode")
				}
				t.Fatalf("create %s userns sandbox: %v", mode, err)
			}
			defer func() { _ = engine.Delete(context.Background(), handle.ID) }()
			if err := engine.Start(ctx, handle.ID); err != nil {
				if mode == "keep-id" && strings.Contains(strings.ToLower(err.Error()), "mqueue") {
					if strict {
						t.Fatalf("required userns keep-id capability is unavailable: %v", err)
					}
					t.Skip("nested crun does not provide the mqueue capability required by keep-id")
				}
				t.Fatalf("start %s userns sandbox: %v", mode, err)
			}
			ownershipDir, err := os.MkdirTemp(workspace, ".podman-userns-"+strings.ReplaceAll(mode, "-", "")+"-")
			if err != nil {
				t.Fatalf("create %s ownership probe directory: %v", mode, err)
			}
			defer os.RemoveAll(ownershipDir)
			if err := os.Chmod(ownershipDir, 0o777); err != nil {
				t.Fatalf("make %s ownership probe directory writable: %v", mode, err)
			}
			marker := filepath.Join(ownershipDir, "created-in-userns")
			guestMarker := filepath.Join("/workspace", filepath.Base(ownershipDir), filepath.Base(marker))
			probe := `set -eu
uid=$(id -u); gid=$(id -g)
{
	printf 'identity=%s:%s\n' "$uid" "$gid"
	printf 'uid_map\n'
	cat /proc/self/uid_map
	printf 'gid_map\n'
	cat /proc/self/gid_map
} > "$1"`
			exec, err := engineClient.ExecCreate(ctx, string(handle.ID), client.ExecCreateOptions{
				Cmd: []string{"sh", "-c", probe, "sh", guestMarker}, User: "1000:1000", AttachStdout: true, AttachStderr: true,
			})
			if err != nil {
				t.Fatalf("create %s userns identity probe: %v", mode, err)
			}
			attached, err := engineClient.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
			if err != nil {
				t.Fatalf("attach %s userns identity probe: %v", mode, err)
			}
			_, copyErr := stdcopy.StdCopy(io.Discard, io.Discard, attached.Reader)
			attached.Close()
			if copyErr != nil {
				t.Fatalf("read %s userns identity probe: %v", mode, copyErr)
			}
			execState, err := engineClient.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
			if err != nil || execState.ExitCode != 0 {
				t.Fatalf("%s userns identity probe exit=%d err=%v", mode, execState.ExitCode, err)
			}
			var guestUID, guestGID, mappedUID, mappedGID uint64
			probeOutput, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("read host-side %s userns probe output: %v", mode, err)
			}
			if n, err := fmt.Sscanf(string(probeOutput), "identity=%d:%d", &guestUID, &guestGID); err != nil || n != 2 {
				t.Fatalf("%s userns identity probe output %q: scanned %d fields, err=%v", mode, probeOutput, n, err)
			}
			var mapSection string
			for _, line := range strings.Split(string(probeOutput), "\n") {
				if line == "uid_map" {
					mapSection = "uid"
					continue
				}
				if line == "gid_map" {
					mapSection = "gid"
					continue
				}
				fields := strings.Fields(line)
				if len(fields) != 3 {
					continue
				}
				containerID, e1 := strconv.ParseUint(fields[0], 10, 64)
				hostID, e2 := strconv.ParseUint(fields[1], 10, 64)
				size, e3 := strconv.ParseUint(fields[2], 10, 64)
				if e1 != nil || e2 != nil || e3 != nil || size == 0 {
					continue
				}
				if mapSection == "uid" && guestUID >= containerID && guestUID-containerID < size {
					mappedUID = hostID + guestUID - containerID
				} else if mapSection == "gid" && guestGID >= containerID && guestGID-containerID < size {
					mappedGID = hostID + guestGID - containerID
				}
			}
			if mappedUID == 0 && guestUID != 0 || mappedGID == 0 && guestGID != 0 {
				t.Fatalf("%s userns maps do not include workload identity: %q", mode, probeOutput)
			}
			if guestUID != 1000 || guestGID != 1000 {
				t.Fatalf("%s workload identity = %d:%d; fixture requires 1000:1000", mode, guestUID, guestGID)
			}
			if mappedUID > uint64(^uint32(0)) {
				t.Fatalf("%s mapped UID %d is outside supported host ID range", mode, mappedUID)
			}
			if mappedGID > uint64(^uint32(0)) {
				t.Fatalf("%s mapped GID %d is outside supported host ID range", mode, mappedGID)
			}
			info, err := os.Stat(marker)
			if err != nil {
				t.Fatalf("stat host-side %s ownership marker: %v; probe=%q", mode, err, probeOutput)
			}
			owner, ok := hostFileOwner(info)
			if !ok {
				t.Fatalf("host-side %s ownership marker has unsupported stat type %T", mode, info.Sys())
			}
			expectedUID, expectedGID := mappedUID, mappedGID
			if rootless {
				subordinateStart, err := strconv.ParseUint(os.Getenv("QUARSHELL_PODMAN_ROOTLESS_SUBUID_START"), 10, 32)
				if err != nil || subordinateStart == 0 {
					t.Fatalf("rootless userns ownership probe requires a valid QUARSHELL_PODMAN_ROOTLESS_SUBUID_START: %v", err)
				}
				expectedUID = rootlessHostID(mode, mappedUID, subordinateStart)
				expectedGID = rootlessHostID(mode, mappedGID, subordinateStart)
			}
			if owner.uid != expectedUID || owner.gid != expectedGID {
				t.Fatalf("%s bind ownership mismatch: guest maps workload 1000:1000 to host %d:%d, file owner is %d:%d; maps=%q", mode, expectedUID, expectedGID, owner.uid, owner.gid, probeOutput)
			}
		})
	}
}

func rootlessHostID(mode string, mappedID, subordinateStart uint64) uint64 {
	if mappedID == 0 {
		return 1000 // rootless Podman runs as the subordinate-ID account.
	}
	if mode == "host" {
		return mappedID // host mode's map already reports the final host ID.
	}
	return subordinateStart + mappedID - 1
}

func TestRootlessHostIDTranslation(t *testing.T) {
	for _, test := range []struct {
		mode   string
		mapped uint64
		want   uint64
	}{
		{mode: "auto", mapped: 1001, want: 201000},
		{mode: "no-map", mapped: 1001, want: 201000},
		{mode: "keep-id", mapped: 0, want: 1000},
		{mode: "host", mapped: 200999, want: 200999},
	} {
		if got := rootlessHostID(test.mode, test.mapped, 200000); got != test.want {
			t.Errorf("rootlessHostID(%q, %d) = %d, want %d", test.mode, test.mapped, got, test.want)
		}
	}
}
