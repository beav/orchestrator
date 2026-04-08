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
	Short: "Show Claude transcript for a task",
	Long: `Shows the stream-json transcript from a Claude session.

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

		runner := sandbox.NewFromConfig(cfg.SandboxBackend, cfg.GjollEnv, cfg.PodmanImage, cfg.AnthropicKeyFile, 19090)
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

// formatTranscriptLine formats a single stream-json line for human readability.
// When verbose is true, thinking blocks are included in the output.
func formatTranscriptLine(line []byte, verbose bool) string {
	var msg struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message struct {
			Content []struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
				Thinking string          `json:"thinking"`
				Content  json.RawMessage `json:"content"` // tool_result: string or array
			} `json:"content"`
		} `json:"message"`
		DurationMS   int     `json:"duration_ms"`
		NumTurns     int     `json:"num_turns"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return ""
	}

	var out string
	switch msg.Type {
	case "assistant":
		for _, c := range msg.Message.Content {
			switch c.Type {
			case "text":
				out += c.Text + "\n"
			case "tool_use":
				summary := toolInputSummary(c.Name, c.Input)
				if summary != "" {
					out += fmt.Sprintf("[tool] %s: %s\n", c.Name, summary)
				} else {
					out += fmt.Sprintf("[tool] %s\n", c.Name)
				}
			case "thinking":
				if verbose && c.Thinking != "" {
					out += fmt.Sprintf("[thinking] %s\n", c.Thinking)
				}
			}
		}
	case "user":
		for _, c := range msg.Message.Content {
			if c.Type != "tool_result" || len(c.Content) == 0 {
				continue
			}
			var s string
			if json.Unmarshal(c.Content, &s) == nil && s != "" {
				out += fmt.Sprintf("  → %s\n", firstLine(s, 200))
			}
		}
	case "result":
		subtype := msg.Subtype
		if subtype == "" {
			subtype = "done"
		}
		duration := float64(msg.DurationMS) / 1000
		if msg.TotalCostUSD > 0 {
			out = fmt.Sprintf("[result] %s (%d turns, %.1fs, $%.2f)\n", subtype, msg.NumTurns, duration, msg.TotalCostUSD)
		} else if msg.DurationMS > 0 {
			out = fmt.Sprintf("[result] %s (%d turns, %.1fs)\n", subtype, msg.NumTurns, duration)
		} else {
			out = fmt.Sprintf("[result] %s\n", subtype)
		}
	}
	return out
}

// toolInputSummary extracts a short description from a tool's input.
func toolInputSummary(name string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}

	switch name {
	case "Write", "Read", "Edit":
		if v, ok := m["file_path"].(string); ok {
			return v
		}
	case "Bash":
		if v, ok := m["description"].(string); ok {
			return v
		}
		if v, ok := m["command"].(string); ok {
			return firstLine(v, 80)
		}
	case "Grep", "Glob":
		if v, ok := m["pattern"].(string); ok {
			return v
		}
	}

	// Fallback: try common field names
	for _, key := range []string{"path", "query", "url", "name"} {
		if v, ok := m[key].(string); ok {
			return firstLine(v, 80)
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
