package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZhuzhuNo3/transferlanes/internal/probe"
	"github.com/ZhuzhuNo3/transferlanes/internal/throughput"
)

func TestHelpPagesDescribePublicUsage(t *testing.T) {
	tests := []struct {
		page     HelpPage
		usage    string
		sections []string
		options  []string
		defaults map[string]string
		viewArg  bool
	}{
		{
			page:     RootHelpPage,
			usage:    "transferlanes <command> [options]",
			sections: []string{"Commands:", "Options:"},
			options:  []string{"-h, --help", "--version"},
		},
		{
			page:     ListHelpPage,
			usage:    "transferlanes list [options]",
			sections: []string{"Selection:", "Diagnostics:", "Options:"},
			options: []string{
				"--network <IPv4>", "--all", "--probe", "--probe-url <URL>",
				"--measure", "--measure-url <URL>", "--measure-duration <time>",
			},
			defaults: map[string]string{
				"--probe-url <URL>":         probe.DefaultEndpointURL,
				"--measure-url <URL>":       throughput.DefaultEndpointURL,
				"--measure-duration <time>": fmt.Sprint(defaultMeasureDuration),
			},
		},
		{
			page:     RunHelpPage,
			usage:    "transferlanes run --source <directory> --network <IPv4>[@weight]...",
			viewArg:  true,
			sections: []string{"Source:", "Networks:", "Command:", "Display and logs:", "Options:"},
			options: []string{
				"--source <directory>", "--follow-symlinks", "--network <IPv4>[@weight]",
				"--auto-weight", "--measure-url <URL>", "--measure-duration <time>",
				"--dns <IP>", "--no-tui", "--mouse", "--log-dir <directory>",
			},
			defaults: map[string]string{
				"--measure-url <URL>":       throughput.DefaultEndpointURL,
				"--measure-duration <time>": fmt.Sprint(defaultMeasureDuration),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.page.String(), func(t *testing.T) {
			got := test.page.Text()
			if strings.TrimSpace(got) == "" || !strings.HasSuffix(got, "\n") {
				t.Fatal("help page is empty or missing a final newline")
			}
			if strings.Contains(got, "\x1b") {
				t.Fatal("help page contains an escape character")
			}
			for lineNumber, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
				if len(line) > 80 {
					t.Errorf("line %d exceeds 80 columns: %q", lineNumber+1, line)
				}
			}
			if !strings.Contains(got, "Usage:\n  "+test.usage) {
				t.Errorf("usage does not describe %q", test.usage)
			}
			if test.viewArg && !strings.Contains(helpUsageBlock(got), "{}") {
				t.Error("run usage omits the required view argument")
			}
			for _, section := range test.sections {
				if !hasHelpLine(got, section) {
					t.Errorf("missing section %q", section)
				}
			}
			for _, option := range test.options {
				if !hasHelpOption(got, option) {
					t.Errorf("missing option syntax %q", option)
				}
			}
			for option, value := range test.defaults {
				if !strings.Contains(helpOptionBlock(got, option), "Default: "+value) {
					t.Errorf("%s does not show rendered default %q", option, value)
				}
			}
			if strings.Contains(got, "%!") || strings.Contains(got, "%s") {
				t.Error("help page contains an unrendered template value")
			}
		})
	}
}

func hasHelpLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func hasHelpOption(text, option string) bool {
	return helpOptionBlock(text, option) != ""
}

func helpUsageBlock(text string) string {
	_, after, found := strings.Cut(text, "Usage:\n")
	if !found {
		return ""
	}
	block, _, _ := strings.Cut(after, "\n\n")
	return block
}

func helpOptionBlock(text, option string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		prefix := "  " + option
		if line != prefix && !strings.HasPrefix(line, prefix+" ") {
			continue
		}
		block := []string{line}
		for _, continuation := range lines[index+1:] {
			if !strings.HasPrefix(continuation, "      ") {
				break
			}
			block = append(block, continuation)
		}
		return strings.Join(block, "\n")
	}
	return ""
}
