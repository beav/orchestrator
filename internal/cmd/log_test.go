package cmd

import (
	"bytes"
	"testing"
)

func TestFormatTranscriptLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		verbose bool
		want    string
	}{
		{
			name: "text content",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]}}`,
			want: "Hello world\n",
		},
		{
			name: "toolRequest without arguments",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"toolRequest","toolCall":{"status":"success","value":{"name":"Read","arguments":{}}}}]}}`,
			want: "[tool] Read\n",
		},
		{
			name: "toolRequest with file_path",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"toolRequest","toolCall":{"status":"success","value":{"name":"Write","arguments":{"file_path":"/home/fedora/hello.txt","content":"hi"}}}}]}}`,
			want: "[tool] Write: /home/fedora/hello.txt\n",
		},
		{
			name: "toolRequest with command",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"toolRequest","toolCall":{"status":"success","value":{"name":"Bash","arguments":{"command":"ls -la"}}}}]}}`,
			want: "[tool] Bash: ls -la\n",
		},
		{
			name: "toolRequest with pattern",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"toolRequest","toolCall":{"status":"success","value":{"name":"Grep","arguments":{"pattern":"func main"}}}}]}}`,
			want: "[tool] Grep: func main\n",
		},
		{
			name: "toolRequest with path fallback",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"toolRequest","toolCall":{"status":"success","value":{"name":"mcp__orchestrator__open_pr","arguments":{"path":"~/project"}}}}]}}`,
			want: "[tool] mcp__orchestrator__open_pr: ~/project\n",
		},
		{
			name: "mixed text and toolRequest",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"Reading file"},{"type":"toolRequest","toolCall":{"status":"success","value":{"name":"Edit","arguments":{"file_path":"main.go"}}}}]}}`,
			want: "Reading file\n[tool] Edit: main.go\n",
		},
		{
			name: "toolResponse with text",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"toolResponse","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"File created successfully"}],"isError":false}}}]}}`,
			want: "  → File created successfully\n",
		},
		{
			name: "toolResponse multiline shows first line",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"toolResponse","toolResult":{"status":"success","value":{"content":[{"type":"text","text":"[master abc1234] Add file\n 1 file changed"}],"isError":false}}}]}}`,
			want: "  → [master abc1234] Add file\n",
		},
		{
			name: "toolResponse empty content",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"toolResponse","toolResult":{"status":"success","value":{"content":[],"isError":false}}}]}}`,
			want: "",
		},
		{
			name: "complete with tokens",
			line: `{"type":"complete","total_tokens":1234}`,
			want: "[complete] total_tokens: 1234\n",
		},
		{
			name: "complete without tokens",
			line: `{"type":"complete"}`,
			want: "[complete]\n",
		},
		{
			name: "error event",
			line: `{"type":"error","error":"something went wrong"}`,
			want: "[error] something went wrong\n",
		},
		{
			name: "notification hidden by default",
			line: `{"type":"notification","data":"extension loaded"}`,
			want: "",
		},
		{
			name:    "notification shown when verbose",
			line:    `{"type":"notification","data":"extension loaded"}`,
			verbose: true,
			want:    "[notification] {\"type\":\"notification\",\"data\":\"extension loaded\"}\n",
		},
		{
			name: "thinking hidden by default",
			line: `{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Let me think about this."}]}}`,
			want: "",
		},
		{
			name:    "thinking shown when verbose",
			line:    `{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Let me think about this."}]}}`,
			verbose: true,
			want:    "[thinking] Let me think about this.\n",
		},
		{
			name:    "mixed with thinking verbose",
			line:    `{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Planning..."},{"type":"text","text":"Here is my answer"}]}}`,
			verbose: true,
			want:    "[thinking] Planning...\nHere is my answer\n",
		},
		{
			name: "message with null message field",
			line: `{"type":"message"}`,
			want: "",
		},
		{
			name: "unknown type ignored",
			line: `{"type":"system","message":"hello"}`,
			want: "",
		},
		{
			name: "invalid json ignored",
			line: `not json at all`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTranscriptLine([]byte(tt.line), tt.verbose)
			if got != tt.want {
				t.Errorf("formatTranscriptLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTranscriptWriter(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
		writes  []string
		want    string
	}{
		{
			name: "single complete line",
			writes: []string{
				`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n",
			},
			want: "hi\n",
		},
		{
			name: "line split across writes",
			writes: []string{
				`{"type":"messa`,
				`ge","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n",
			},
			want: "hi\n",
		},
		{
			name: "multiple lines in one write",
			writes: []string{
				`{"type":"complete"}` + "\n" +
					`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n",
			},
			want: "[complete]\ndone\n",
		},
		{
			name: "skips unknown types",
			writes: []string{
				`{"type":"system"}` + "\n" +
					`{"type":"complete"}` + "\n",
			},
			want: "[complete]\n",
		},
		{
			name:    "verbose passes through to formatter",
			verbose: true,
			writes: []string{
				`{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"}]}}` + "\n",
			},
			want: "[thinking] hmm\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := newTranscriptWriter(&buf, tt.verbose)
			for _, w := range tt.writes {
				if _, err := tw.Write([]byte(w)); err != nil {
					t.Fatalf("Write() error: %v", err)
				}
			}
			if got := buf.String(); got != tt.want {
				t.Errorf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolRequestSummary(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantName    string
		wantSummary string
	}{
		{
			name:        "file_path argument",
			input:       `{"status":"success","value":{"name":"Write","arguments":{"file_path":"/home/fedora/hello.txt","content":"hi"}}}`,
			wantName:    "Write",
			wantSummary: "/home/fedora/hello.txt",
		},
		{
			name:        "command argument",
			input:       `{"status":"success","value":{"name":"Bash","arguments":{"command":"ls -la"}}}`,
			wantName:    "Bash",
			wantSummary: "ls -la",
		},
		{
			name:        "description preferred over command",
			input:       `{"status":"success","value":{"name":"Bash","arguments":{"command":"git status","description":"Show git status"}}}`,
			wantName:    "Bash",
			wantSummary: "Show git status",
		},
		{
			name:        "pattern argument",
			input:       `{"status":"success","value":{"name":"Grep","arguments":{"pattern":"func main"}}}`,
			wantName:    "Grep",
			wantSummary: "func main",
		},
		{
			name:        "path fallback",
			input:       `{"status":"success","value":{"name":"mcp__open_pr","arguments":{"path":"~/project"}}}`,
			wantName:    "mcp__open_pr",
			wantSummary: "~/project",
		},
		{
			name:        "no matching arguments",
			input:       `{"status":"success","value":{"name":"Read","arguments":{"foo":"bar"}}}`,
			wantName:    "Read",
			wantSummary: "",
		},
		{
			name:        "empty arguments",
			input:       `{"status":"success","value":{"name":"Read","arguments":{}}}`,
			wantName:    "Read",
			wantSummary: "",
		},
		{
			name:        "no arguments",
			input:       `{"status":"success","value":{"name":"Read"}}`,
			wantName:    "Read",
			wantSummary: "",
		},
		{
			name:        "empty input",
			input:       ``,
			wantName:    "",
			wantSummary: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotSummary := toolRequestSummary([]byte(tt.input))
			if gotName != tt.wantName {
				t.Errorf("toolRequestSummary() name = %q, want %q", gotName, tt.wantName)
			}
			if gotSummary != tt.wantSummary {
				t.Errorf("toolRequestSummary() summary = %q, want %q", gotSummary, tt.wantSummary)
			}
		})
	}
}

func TestToolResponseSummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "text content",
			input: `{"status":"success","value":{"content":[{"type":"text","text":"File created successfully"}],"isError":false}}`,
			want:  "File created successfully",
		},
		{
			name:  "multiline text shows first line",
			input: `{"status":"success","value":{"content":[{"type":"text","text":"[master abc1234] Add file\n 1 file changed"}],"isError":false}}`,
			want:  "[master abc1234] Add file",
		},
		{
			name:  "empty content array",
			input: `{"status":"success","value":{"content":[],"isError":false}}`,
			want:  "",
		},
		{
			name:  "no text content",
			input: `{"status":"success","value":{"content":[{"type":"image","data":"..."}],"isError":false}}`,
			want:  "",
		},
		{
			name:  "empty input",
			input: ``,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolResponseSummary([]byte(tt.input))
			if got != tt.want {
				t.Errorf("toolResponseSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{name: "short single line", s: "hello", max: 80, want: "hello"},
		{name: "multiline", s: "first\nsecond\nthird", max: 80, want: "first"},
		{name: "truncated", s: "a very long string", max: 10, want: "a very lon…"},
		{name: "multiline and truncated", s: "abcdefghij\nsecond", max: 5, want: "abcde…"},
		{name: "empty", s: "", max: 80, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstLine(tt.s, tt.max)
			if got != tt.want {
				t.Errorf("firstLine() = %q, want %q", got, tt.want)
			}
		})
	}
}
