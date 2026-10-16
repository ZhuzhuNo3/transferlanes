// transferchild is an executable fixture for transferlanes integration tests.
// It deliberately has no dependency on transferlanes internals.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const (
	usageExit = 64
	ioExit    = 74
	stageExit = 75
)

type repeatedStrings []string

func (values *repeatedStrings) String() string { return fmt.Sprint([]string(*values)) }
func (values *repeatedStrings) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type readEvidence struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type evidence struct {
	WorkingDirectory string         `json:"workingDirectory"`
	ViewPath         string         `json:"viewPath"`
	ViewFiles        []string       `json:"viewFiles"`
	ViewReadOnly     bool           `json:"viewReadOnly"`
	ViewWriteError   string         `json:"viewWriteError,omitempty"`
	Reads            []readEvidence `json:"reads"`
}

type options struct {
	reads             repeatedStrings
	stdoutLines       repeatedStrings
	stderrLines       repeatedStrings
	outputDir         string
	appendFile        string
	appendText        string
	evidenceFile      string
	evidencePerView   bool
	stdoutProgressOne string
	stdoutProgressTwo string
	stderrProgressOne string
	stderrProgressTwo string
	ansi              bool
	readyFile         string
	releaseFile       string
	stageTimeout      time.Duration
	exitCode          int
	viewPath          string
}

func parseOptions(args []string) (options, error) {
	var result options
	flags := flag.NewFlagSet("transferchild", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&result.reads, "read", "read a file relative to the working directory; repeatable")
	flags.StringVar(&result.outputDir, "output-dir", "", "create this output directory")
	flags.StringVar(&result.appendFile, "append-file", "", "append to this file")
	flags.StringVar(&result.appendText, "append-text", "", "text to append")
	flags.StringVar(&result.evidenceFile, "evidence", "", "write JSON evidence to this file")
	flags.BoolVar(&result.evidencePerView, "evidence-per-view", false, "suffix evidence path with the view transfer name")
	flags.StringVar(&result.stdoutProgressOne, "stdout-progress-first", "", "initial stdout progress")
	flags.StringVar(&result.stdoutProgressTwo, "stdout-progress-last", "", "final stdout progress")
	flags.StringVar(&result.stderrProgressOne, "stderr-progress-first", "", "initial stderr progress")
	flags.StringVar(&result.stderrProgressTwo, "stderr-progress-last", "", "final stderr progress")
	flags.Var(&result.stdoutLines, "stdout-line", "append a line to stdout; repeatable")
	flags.Var(&result.stderrLines, "stderr-line", "append a line to stderr; repeatable")
	flags.BoolVar(&result.ansi, "ansi", false, "style progress with ANSI green")
	flags.StringVar(&result.readyFile, "ready-file", "", "create this file before waiting")
	flags.StringVar(&result.releaseFile, "release-file", "", "wait for this file before finishing")
	flags.DurationVar(&result.stageTimeout, "stage-timeout", 30*time.Second, "maximum release wait")
	flags.IntVar(&result.exitCode, "exit-code", 0, "intentional final exit code (0-125)")
	if err := flags.Parse(args); err != nil {
		return result, err
	}
	if flags.NArg() != 1 || !filepath.IsAbs(flags.Arg(0)) {
		return result, errors.New("expected exactly one absolute view path")
	}
	result.viewPath = flags.Arg(0)
	if result.evidenceFile == "" || result.exitCode < 0 || result.exitCode > 125 {
		return result, errors.New("--evidence is required and --exit-code must be in 0..125")
	}
	if (result.appendFile == "") != (result.appendText == "") {
		return result, errors.New("--append-file and --append-text must be used together")
	}
	if (result.readyFile == "") != (result.releaseFile == "") || result.stageTimeout <= 0 {
		return result, errors.New("stage files must be paired and --stage-timeout must be positive")
	}
	if (result.stdoutProgressOne == "") != (result.stdoutProgressTwo == "") ||
		(result.stderrProgressOne == "") != (result.stderrProgressTwo == "") {
		return result, errors.New("each progress stream needs both first and last text")
	}
	return result, nil
}

func collectView(path string) ([]string, bool, string, error) {
	files := []string{}
	err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, false, "", err
	}
	sort.Strings(files)
	probe, err := os.CreateTemp(path, ".transferchild-probe-")
	if err != nil {
		if errors.Is(err, syscall.EROFS) {
			return files, true, err.Error(), nil
		}
		if errors.Is(err, fs.ErrPermission) {
			return files, false, err.Error(), nil
		}
		return nil, false, "", err
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		return nil, false, "", err
	}
	if err := os.Remove(probePath); err != nil {
		return nil, false, "", err
	}
	return files, false, "", nil
}

func emitProgress(output io.Writer, value string, ansi bool, final bool) error {
	if value == "" {
		return nil
	}
	if ansi {
		value = "\x1b[32m" + value + "\x1b[0m"
	}
	if final {
		_, err := fmt.Fprintf(output, "\r%s\n", value)
		return err
	}
	_, err := fmt.Fprintf(output, "\r%s", value)
	return err
}

func waitForRelease(path string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return fmt.Errorf("release file %q was not created before timeout", path)
		}
	}
}

func execute(opts options) (int, error) {
	wd, err := os.Getwd()
	if err != nil {
		return ioExit, err
	}
	result := evidence{WorkingDirectory: wd, ViewPath: opts.viewPath, Reads: []readEvidence{}}
	for _, path := range opts.reads {
		content, err := os.ReadFile(path)
		if err != nil {
			return ioExit, fmt.Errorf("read %q: %w", path, err)
		}
		result.Reads = append(result.Reads, readEvidence{Path: path, Content: string(content)})
	}
	if opts.outputDir != "" {
		if err := os.MkdirAll(opts.outputDir, 0755); err != nil {
			return ioExit, fmt.Errorf("create output directory %q: %w", opts.outputDir, err)
		}
	}
	if opts.appendFile != "" {
		output, err := os.OpenFile(opts.appendFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return ioExit, fmt.Errorf("open append file %q: %w", opts.appendFile, err)
		}
		_, writeErr := output.WriteString(opts.appendText)
		closeErr := output.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return ioExit, fmt.Errorf("append file %q: %w", opts.appendFile, err)
		}
	}
	result.ViewFiles, result.ViewReadOnly, result.ViewWriteError, err = collectView(opts.viewPath)
	if err != nil {
		return ioExit, fmt.Errorf("inspect view %q: %w", opts.viewPath, err)
	}
	if err := emitProgress(os.Stdout, opts.stdoutProgressOne, opts.ansi, false); err != nil {
		return ioExit, err
	}
	if err := emitProgress(os.Stderr, opts.stderrProgressOne, opts.ansi, false); err != nil {
		return ioExit, err
	}
	if opts.readyFile != "" {
		if err := os.WriteFile(opts.readyFile, []byte("ready\n"), 0644); err != nil {
			return ioExit, fmt.Errorf("create ready file %q: %w", opts.readyFile, err)
		}
		if err := waitForRelease(opts.releaseFile, opts.stageTimeout); err != nil {
			return stageExit, err
		}
	}
	if err := emitProgress(os.Stdout, opts.stdoutProgressTwo, opts.ansi, true); err != nil {
		return ioExit, err
	}
	if err := emitProgress(os.Stderr, opts.stderrProgressTwo, opts.ansi, true); err != nil {
		return ioExit, err
	}
	for _, line := range opts.stdoutLines {
		if _, err := fmt.Fprintln(os.Stdout, line); err != nil {
			return ioExit, err
		}
	}
	for _, line := range opts.stderrLines {
		if _, err := fmt.Fprintln(os.Stderr, line); err != nil {
			return ioExit, err
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return ioExit, err
	}
	data = append(data, '\n')
	evidenceFile := opts.evidenceFile
	if opts.evidencePerView {
		evidenceFile += "." + filepath.Base(filepath.Dir(opts.viewPath))
	}
	if err := os.WriteFile(evidenceFile, data, 0644); err != nil {
		return ioExit, fmt.Errorf("write evidence %q: %w", evidenceFile, err)
	}
	return opts.exitCode, nil
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "transferchild:", err)
		os.Exit(usageExit)
	}
	code, err := execute(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "transferchild:", err)
	}
	os.Exit(code)
}
