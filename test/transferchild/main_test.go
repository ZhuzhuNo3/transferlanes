package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transferchild")
	command := exec.Command("go", "build", "-o", path, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build transferchild: %v\n%s", err, output)
	}
	return path
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("command error: %v", err)
	}
	return exit.ExitCode()
}

func TestTransferChildFilesAndStreams(t *testing.T) {
	binary := fixtureBinary(t)
	root := t.TempDir()
	working := filepath.Join(root, "caller")
	view := filepath.Join(root, "source-view")
	for _, directory := range []string{working, filepath.Join(view, "nested")} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(working, ".ossconfig-ali"), []byte("config\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "nested", "file.txt"), []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary,
		"--read", ".ossconfig-ali",
		"--output-dir", "ossutil_output",
		"--append-file", "ossutil_output/entries.log", "--append-text", "entry\n",
		"--evidence", "evidence.json",
		"--stdout-progress-first", "sending 1", "--stdout-progress-last", "sending 2",
		"--stderr-progress-first", "checking 1", "--stderr-progress-last", "checking 2",
		"--stdout-line", "completed", "--stdout-line", "summary",
		"--stderr-line", "warning", "--ansi",
		"--ready-file", "ready", "--release-file", "release", view)
	command.Dir = working
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(working, "ready")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture never entered ready stage")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(working, "evidence.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("evidence should follow release; stat error=%v", err)
	}
	if err := os.WriteFile(filepath.Join(working, "release"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("fixture failed: %v; stderr=%q", err, stderr.String())
	}
	if got, want := stdout.String(), "\r\x1b[32msending 1\x1b[0m\r\x1b[32msending 2\x1b[0m\ncompleted\nsummary\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "\r\x1b[32mchecking 1\x1b[0m\r\x1b[32mchecking 2\x1b[0m\nwarning\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if content, err := os.ReadFile(filepath.Join(working, "ossutil_output", "entries.log")); err != nil || string(content) != "entry\n" {
		t.Errorf("append file = %q, %v", content, err)
	}
	data, err := os.ReadFile(filepath.Join(working, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var observed evidence
	if err := json.Unmarshal(data, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.WorkingDirectory != working || observed.ViewPath != view || observed.ViewReadOnly ||
		!reflect.DeepEqual(observed.ViewFiles, []string{"nested/file.txt"}) ||
		!reflect.DeepEqual(observed.Reads, []readEvidence{{Path: ".ossconfig-ali", Content: "config\n"}}) {
		t.Errorf("unexpected evidence: %+v", observed)
	}
	if entries, err := os.ReadDir(view); err != nil || len(entries) != 1 {
		t.Errorf("view probe left files behind: entries=%v err=%v", entries, err)
	}
}

func TestTransferChildFailures(t *testing.T) {
	binary := fixtureBinary(t)
	working := t.TempDir()
	view := t.TempDir()
	for _, scenario := range []struct {
		name string
		args []string
		code int
		text string
	}{
		{name: "missing view", args: []string{"--evidence", "evidence.json"}, code: usageExit, text: "expected exactly one absolute view path"},
		{name: "missing input", args: []string{"--read", "missing", "--evidence", "evidence.json", view}, code: ioExit, text: "read \"missing\""},
		{name: "per-view evidence write", args: []string{"--evidence", "missing/evidence.json", "--evidence-per-view", view},
			code: ioExit, text: "write evidence \"missing/evidence.json." + filepath.Base(filepath.Dir(view)) + "\""},
		{name: "stage timeout", args: []string{"--evidence", "evidence.json", "--ready-file", "ready", "--release-file", "release", "--stage-timeout", "20ms", view}, code: stageExit, text: "before timeout"},
		{name: "chosen failure", args: []string{"--evidence", "evidence.json", "--exit-code", "23", view}, code: 23},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			command := exec.Command(binary, scenario.args...)
			command.Dir = working
			output, err := command.CombinedOutput()
			if got := exitCode(t, err); got != scenario.code {
				t.Errorf("exit code=%d want=%d; output=%q", got, scenario.code, output)
			}
			if !strings.Contains(string(output), scenario.text) {
				t.Errorf("output=%q missing %q", output, scenario.text)
			}
		})
	}
}

func TestTransferChildConcurrentSharedOutputDirectory(t *testing.T) {
	binary := fixtureBinary(t)
	working := t.TempDir()
	if err := os.WriteFile(filepath.Join(working, ".ossconfig-ali"), []byte("shared"), 0644); err != nil {
		t.Fatal(err)
	}
	type child struct {
		command *exec.Cmd
		view    string
	}
	children := make([]child, 3)
	for i := range children {
		view := filepath.Join(t.TempDir(), "view")
		if err := os.Mkdir(view, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(view, "lane.txt"), []byte{byte('1' + i)}, 0644); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "--read", ".ossconfig-ali",
			"--output-dir", "ossutil_output", "--append-file", filepath.Join("ossutil_output", string(rune('1'+i))+".log"),
			"--append-text", "done\n", "--evidence", string(rune('1'+i))+".json", view)
		command.Dir = working
		children[i] = child{command: command, view: view}
	}
	for i := range children {
		if err := children[i].command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, child := range children {
		if err := child.command.Wait(); err != nil {
			t.Errorf("child %d: %v", i+1, err)
			continue
		}
		name := string(rune('1' + i))
		if data, err := os.ReadFile(filepath.Join(working, "ossutil_output", name+".log")); err != nil || string(data) != "done\n" {
			t.Errorf("child %s append: %q, %v", name, data, err)
		}
		data, err := os.ReadFile(filepath.Join(working, name+".json"))
		if err != nil {
			t.Error(err)
			continue
		}
		var observed evidence
		if err := json.Unmarshal(data, &observed); err != nil {
			t.Error(err)
			continue
		}
		if observed.WorkingDirectory != working || observed.ViewPath != child.view ||
			!reflect.DeepEqual(observed.ViewFiles, []string{"lane.txt"}) ||
			!reflect.DeepEqual(observed.Reads, []readEvidence{{Path: ".ossconfig-ali", Content: "shared"}}) {
			t.Errorf("child %s evidence: %+v", name, observed)
		}
	}
}
