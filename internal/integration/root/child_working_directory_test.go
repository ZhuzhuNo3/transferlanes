//go:build linux && rootintegration

package root_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type transferChildEvidence struct {
	WorkingDirectory string   `json:"workingDirectory"`
	ViewPath         string   `json:"viewPath"`
	ViewFiles        []string `json:"viewFiles"`
	ViewReadOnly     bool     `json:"viewReadOnly"`
	Reads            []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"reads"`
}

func workingDirectoryFixture(t *testing.T, network *hostNetworkFixture) (string, string) {
	t.Helper()
	scenario := network.newRunSupervisorScenario(t, "cwd-fixture")
	caller := filepath.Join(scenario.markers, "caller")
	if err := os.Mkdir(caller, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caller, ".ossconfig-ali"), []byte("relative-config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("TRANSFERLANES_TEST_TRANSFERCHILD")
	if binary == "" {
		t.Fatal("transferchild fixture is unavailable")
	}
	return caller, binary
}

func workingDirectoryArguments(fixture *hostNetworkFixture, sourceCount int, auto bool, logDir string) []string {
	args := []string{"run", "--no-tui", "--source", os.Getenv(supervisorTransferEnv), "--log-dir", logDir}
	for _, source := range fixture.sources[:sourceCount] {
		args = append(args, "--network", source.String())
	}
	if auto {
		args = append(args, "--auto-weight", "--measure-url", "http://"+net.JoinHostPort(fixture.remote.String(), "18081")+"/measure", "--measure-duration", "2s")
	}
	return args
}

func workingDirectoryChildArguments(executable string, staged, stderrProgress bool) []string {
	args := []string{executable, "--read", ".ossconfig-ali", "--output-dir", "ossutil_output",
		"--append-file", "ossutil_output/entries.log", "--append-text", "entry\n",
		"--evidence", "evidence.json", "--evidence-per-view",
		"--stdout-progress-first", "sending 1", "--stdout-progress-last", "sending 2",
		"--stdout-line", "completed", "--stderr-line", "warning", "--ansi"}
	if stderrProgress {
		args = append(args, "--stderr-progress-first", "checking 1", "--stderr-progress-last", "checking 2")
	}
	if staged {
		args = append(args, "--ready-file", "ready", "--release-file", "release")
	}
	return append(args, "{}")
}

func assertWorkingDirectoryEvidence(t *testing.T, caller string, count int) {
	t.Helper()
	all := make(map[string]bool)
	for number := 1; number <= count; number++ {
		path := filepath.Join(caller, fmt.Sprintf("evidence.json.transfer-%03d", number))
		encoded, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var observed transferChildEvidence
		if err := json.Unmarshal(encoded, &observed); err != nil {
			t.Fatal(err)
		}
		if observed.WorkingDirectory != caller || !validPrivateViewAlias(observed.ViewPath) ||
			filepath.Base(filepath.Dir(observed.ViewPath)) != fmt.Sprintf("transfer-%03d", number) ||
			!observed.ViewReadOnly || len(observed.Reads) != 1 ||
			observed.Reads[0].Path != ".ossconfig-ali" || observed.Reads[0].Content != "relative-config\n" {
			t.Fatalf("transfer %d evidence = %+v", number, observed)
		}
		for _, member := range observed.ViewFiles {
			if all[member] {
				t.Fatalf("source member %s appeared in more than one view", member)
			}
			all[member] = true
		}
	}
	if count == 3 {
		want := make([]string, 0, len(transferSourceContents()))
		got := make([]string, 0, len(all))
		for member := range transferSourceContents() {
			want = append(want, member)
		}
		for member := range all {
			got = append(got, member)
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("view member union=%v, want %v", got, want)
		}
	}
}

func TestPublicChildInheritsCallerDirectory(t *testing.T) {
	for _, test := range []struct {
		name       string
		count      int
		auto       bool
		executable string
	}{
		{"manual-single-relative-executable", 1, false, "./program"},
		{"manual-three-relative-path", 3, false, "program"},
		{"automatic-three", 3, true, "./program"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHostNetworkFixture(t)
			fixture.install()
			baseline := fixture.hostSurface()
			runsBefore := authorityEntries(t)
			caller, binary := workingDirectoryFixture(t, fixture)
			if err := os.Link(binary, filepath.Join(caller, "program")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(caller, "bin"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(binary, filepath.Join(caller, "bin", "program")); err != nil {
				t.Fatal(err)
			}
			logDir := filepath.Join(caller, "logs")
			args := append(workingDirectoryArguments(fixture, test.count, test.auto, logDir), "--")
			args = append(args, workingDirectoryChildArguments(test.executable, false, true)...)
			marker := "TRANSFERLANES_CWD_TEST=" + filepath.Base(caller)
			env := []string{marker, "PATH=bin:" + os.Getenv("PATH")}
			var action func(*exec.Cmd) error
			if test.auto {
				action = func(_ *exec.Cmd) error { return waitForThroughputHelperCWD(t, marker, caller, 15*time.Second) }
			}
			stdout, stderr, err := runPublicTransferLanesPipesInDirectory(t, args, caller, env, action)
			if err != nil {
				t.Fatalf("run failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
			}
			assertCompletedTransfers(t, stdout, test.count)
			assertWorkingDirectoryEvidence(t, caller, test.count)
			entries, err := os.ReadFile(filepath.Join(caller, "ossutil_output", "entries.log"))
			if err != nil || bytes.Count(entries, []byte("entry\n")) != test.count {
				t.Fatalf("shared output entries=%q, error=%v", entries, err)
			}
			for number := 1; number <= test.count; number++ {
				prefix := fmt.Sprintf("[%d %s] ", number, fixture.sources[number-1])
				if !strings.Contains(stdout, prefix+"sending 2\n") || !strings.Contains(stdout, prefix+"completed\n") ||
					!strings.Contains(stderr, prefix+"warning\n") || strings.Contains(stdout, "sending 1") {
					t.Fatalf("plain progress/append output missing: stdout=%q stderr=%q", stdout, stderr)
				}
				raw, err := os.ReadFile(filepath.Join(logDir, fmt.Sprintf("transfer-%02d.stdout.log", number)))
				if err != nil || string(raw) != "\r\x1b[32msending 1\x1b[0m\r\x1b[32msending 2\x1b[0m\ncompleted\n" {
					t.Fatalf("transfer %d raw stdout=%q, error=%v", number, raw, err)
				}
				raw, err = os.ReadFile(filepath.Join(logDir, fmt.Sprintf("transfer-%02d.stderr.log", number)))
				if err != nil || string(raw) != "\r\x1b[32mchecking 1\x1b[0m\r\x1b[32mchecking 2\x1b[0m\nwarning\n" {
					t.Fatalf("transfer %d raw stderr=%q, error=%v", number, raw, err)
				}
			}
			fixture.assertSurface(baseline)
			if after := authorityEntries(t); !slices.Equal(after, runsBefore) {
				t.Fatalf("run roots before=%v after=%v", runsBefore, after)
			}
		})
	}
}

func waitForThroughputHelperCWD(t *testing.T, marker, caller string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, process := range markedProcesses(t, marker) {
			if !bytes.Contains(process.command, []byte("--transferlanes-internal-throughput=")) {
				continue
			}
			cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", process.pid))
			if err == nil {
				if cwd != caller {
					return fmt.Errorf("throughput helper cwd=%q, want %q", cwd, caller)
				}
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("throughput helper was not observed within %s", timeout)
}

func TestPublicInteractiveChildInheritsCallerDirectory(t *testing.T) {
	fixture := newHostNetworkFixture(t)
	fixture.install()
	baseline := fixture.hostSurface()
	runsBefore := authorityEntries(t)
	caller, binary := workingDirectoryFixture(t, fixture)
	args := []string{"run", "--source", os.Getenv(supervisorTransferEnv), "--network", fixture.sources[0].String(),
		"--log-dir", filepath.Join(caller, "logs"), "--"}
	args = append(args, workingDirectoryChildArguments(binary, true, false)...)
	output, err := runPublicTransferLanesPTYCommandInDirectory(args, nil, caller,
		func(_ context.Context, _ *exec.Cmd, _ *os.File, capture *boundedPTYCapture) error {
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(caller, "ready")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("interactive child never reached ready stage: %s", capture.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := waitForCapturedText(capture, "sending 1", 10*time.Second); err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(caller, "evidence.json.transfer-001")); !os.IsNotExist(err) {
				return fmt.Errorf("evidence appeared before release: %v", err)
			}
			return os.WriteFile(filepath.Join(caller, "release"), nil, 0o600)
		})
	if err != nil {
		t.Fatalf("interactive run failed: %v\n%s", err, output)
	}
	assertInteractiveSuccessSnapshotTitles(t, output, fixture.sources[:1])
	tail := finalInteractiveOutput(t, output)
	for _, want := range []string{"sending 2", "completed", "warning"} {
		if !strings.Contains(tail, want) {
			t.Fatalf("interactive final output lacks %q: %s", want, tail)
		}
	}
	if strings.Contains(tail, "sending 1") {
		t.Fatalf("interactive final output retained old progress: %s", tail)
	}
	assertWorkingDirectoryEvidence(t, caller, 1)
	raw, err := os.ReadFile(filepath.Join(caller, "logs", "transfer-01.ptylog"))
	if err != nil || !bytes.Contains(raw, []byte("\r\x1b[32msending 1\x1b[0m\r\x1b[32msending 2\x1b[0m")) {
		t.Fatalf("interactive raw output=%q, error=%v", raw, err)
	}
	fixture.assertSurface(baseline)
	if after := authorityEntries(t); !slices.Equal(after, runsBefore) {
		t.Fatalf("run roots before=%v after=%v", runsBefore, after)
	}
}
