package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/drellabot/orchestrator/internal/sandbox"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/drellabot/orchestrator/internal/config"
	"github.com/drellabot/orchestrator/internal/task"
	"github.com/spf13/cobra"
)

var followFlag bool

var logCmd = &cobra.Command{
	Use:   "log <task-name>",
	Short: "Show agent transcript for a task",
	Long: `Shows the stream-json transcript from a goose session.

Without -f, reads the local transcript from the task directory (after the
task has completed and the transcript has been copied back).

With -f/--follow, tails the live transcript from the running sandbox VM
via SSH, formatted for human readability.

Use -v to also show the model's internal reasoning (thinking blocks).

Examples:
  orchestrator log my-task          Show completed task transcript
  orchestrator log -v my-task       Include model reasoning
  orchestrator log -f my-task       Follow live transcript`,
	Args: cobra.ExactArgs(1),
	RunE: runLog,
}

func init() {
	logCmd.Flags().BoolVarP(&followFlag, "follow", "f", false, "follow live transcript via SSH")
}

func runLog(cmd *cobra.Command, args []string) error {
	taskName := args[0]

	if followFlag {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer cancel()

		cfg, err := config.Load(configPath)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		podmanCfg := &sandbox.PodmanConfig{
			Image:           cfg.PodmanImage,
			AgentProvider:   cfg.AgentProvider,
			AgentModel:      cfg.AgentModel,
			AgentAPIKeyFile: cfg.AgentAPIKeyFile,
			MCPPort:         19090,
		}
		runner := sandbox.NewFromConfig(cfg.SandboxBackend, cfg.GjollEnv, podmanCfg)
		tw := newTranscriptWriter(os.Stdout, verbose)
		return runner.SSHProxyOutput(ctx, taskName, tw, &sandbox.SSHOpts{Proxy: true}, "tail -f ~/transcript.jsonl")
	}

	// Read local transcript
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	transcriptPath := task.TranscriptPathFor(cfg.OutputDir, taskName)
	f, err := os.Open(transcriptPath)
	if err != nil {
		return fmt.Errorf("opening transcript: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		formatted := formatTranscriptLine(scanner.Bytes(), verbose)
		if formatted != "" {
			fmt.Print(formatted)
		}
	}
	return scanner.Err()
}

// transcriptWriter is an io.Writer that buffers input until complete JSONL
// lines are available, then formats each line for human readability.
type transcriptWriter struct {
	w       io.Writer
	buf     []byte
	verbose bool
}

func newTranscriptWriter(w io.Writer, verbose bool) *transcriptWriter {
	return &transcriptWriter{w: w, verbose: verbose}
}

func (tw *transcriptWriter) Write(p []byte) (int, error) {
	tw.buf = append(tw.buf, p...)
	for {
		idx := bytes.IndexByte(tw.buf, '\n')
		if idx < 0 {
			break
		}
		line := tw.buf[:idx]
		tw.buf = tw.buf[idx+1:]
		formatted := formatTranscriptLine(line, tw.verbose)
		if formatted != "" {
			if _, err := io.WriteString(tw.w, formatted); err != nil {
				return 0, err
			}
		}
	}
	return len(p), nil
}

// formatTranscriptLine formats a single goose stream-json line for human readability.
// When verbose is true, thinking blocks and notifications are included in the output.
func formatTranscriptLine(line []byte, verbose bool) string {
	var event struct {
		Type        string `json:"type"` // "message", "error", "complete", "notification"
		Error       string `json:"error"`
		TotalTokens *int   `json:"total_tokens"`
		Message     *struct {
			Role    string `json:"role"`
			Content []struct {
				Type       string          `json:"type"` // "text", "toolRequest", "toolResponse", "thinking"
				Text       string          `json:"text"`
				Thinking   string          `json:"thinking"`
				ID         string          `json:"id"`
				ToolCall   json.RawMessage `json:"toolCall"`
				ToolResult json.RawMessage `json:"toolResult"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return ""
	}

	var out string
	switch event.Type {
	case "message":
		if event.Message == nil {
			return ""
		}
		for _, c := range event.Message.Content {
			switch c.Type {
			case "text":
				out += c.Text + "\n"
			case "toolRequest":
				name, summary := toolRequestSummary(c.ToolCall)
				if summary != "" {
					out += fmt.Sprintf("[tool] %s: %s\n", name, summary)
				} else if name != "" {
					out += fmt.Sprintf("[tool] %s\n", name)
				}
			case "toolResponse":
				result := toolResponseSummary(c.ToolResult)
				if result != "" {
					out += fmt.Sprintf("  → %s\n", result)
				}
			case "thinking":
				if verbose && c.Thinking != "" {
					out += fmt.Sprintf("[thinking] %s\n", c.Thinking)
				}
			}
		}
	case "complete":
		if event.TotalTokens != nil {
			out = fmt.Sprintf("[complete] total_tokens: %d\n", *event.TotalTokens)
		} else {
			out = "[complete]\n"
		}
	case "error":
		out = fmt.Sprintf("[error] %s\n", event.Error)
	case "notification":
		if verbose {
			out = fmt.Sprintf("[notification] %s\n", firstLine(string(line), 200))
		}
	}
	return out
}

// toolRequestSummary extracts the tool name and a short description from a goose toolRequest.
// The toolCall field has structure: {"status":"success","value":{"name":"...","arguments":{...}}}
func toolRequestSummary(raw json.RawMessage) (name, summary string) {
	if len(raw) == 0 {
		return "", ""
	}
	var tc struct {
		Value struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"value"`
	}
	if json.Unmarshal(raw, &tc) != nil {
		return "", ""
	}
	name = tc.Value.Name

	if len(tc.Value.Arguments) == 0 {
		return name, ""
	}

	var args map[string]any
	if json.Unmarshal(tc.Value.Arguments, &args) != nil {
		return name, ""
	}

	// Try common field names for a short summary
	for _, key := range []string{"file_path", "path", "description", "command", "pattern", "query", "url", "name"} {
		if v, ok := args[key].(string); ok {
			return name, firstLine(v, 80)
		}
	}
	return name, ""
}

// toolResponseSummary extracts a short description from a goose toolResponse.
// The toolResult field has structure: {"status":"success","value":{"content":[...],"isError":false}}
func toolResponseSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var tr struct {
		Value struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"value"`
	}
	if json.Unmarshal(raw, &tr) != nil {
		return ""
	}
	for _, c := range tr.Value.Content {
		if c.Type == "text" && c.Text != "" {
			return firstLine(c.Text, 200)
		}
	}
	return ""
}

// firstLine returns the first line of s, truncated to max characters.
func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
