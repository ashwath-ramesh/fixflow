package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CLIProvider invokes an LLM via its CLI tool (claude or codex).
type CLIProvider struct {
	name string // "claude" or "codex"
}

func NewCLIProvider(name string) *CLIProvider {
	return &CLIProvider{name: name}
}

func (p *CLIProvider) Name() string { return p.name }

func (p *CLIProvider) Run(ctx context.Context, workDir, prompt string) (Response, error) {
	start := time.Now()

	// Store JSONL outside the work dir so it doesn't pollute git status,
	// but in a persistent location so the TUI can display session details.
	jsonlDir := filepath.Join(filepath.Dir(workDir), "sessions")
	_ = os.MkdirAll(jsonlDir, 0o755)
	jsonlFile := filepath.Join(jsonlDir, fmt.Sprintf("session-%d.jsonl", time.Now().UnixNano()))

	args := p.buildArgs(prompt, jsonlFile)

	slog.Debug("llm exec", "provider", p.name, "workdir", workDir, "args_count", len(args))

	cmd := exec.CommandContext(ctx, p.name, args...)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Response{}, fmt.Errorf("stdout pipe: %w", err)
	}
	// Discard stderr — LLM tools emit noisy internal warnings (e.g. codex rollout state errors).
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return Response{}, fmt.Errorf("start %s: %w", p.name, err)
	}

	// Read streaming JSONL output and capture the final text.
	var resp Response
	resp.JSONLPath = jsonlFile

	// Open JSONL file once for the entire session.
	jsonlF, jsonlErr := os.OpenFile(jsonlFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if jsonlErr != nil {
		slog.Warn("failed to open jsonl file", "path", jsonlFile, "err", jsonlErr)
	}
	defer func() {
		if jsonlF != nil {
			jsonlF.Close()
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // 1MB line buffer
	var lastText string
	var totalIn, totalOut int

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Write to JSONL file.
		if jsonlF != nil {
			if _, err := jsonlF.WriteString(line + "\n"); err != nil {
				slog.Warn("failed to write jsonl line", "err", err)
			}
		}

		var msg jsonlMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		switch {
		// Claude format: assistant messages with content blocks.
		case msg.Type == "assistant" && msg.Message.Content != nil:
			for _, block := range msg.Message.Content {
				if block.Type == "text" && block.Text != "" {
					lastText = block.Text
				}
			}
			if msg.Message.Usage.InputTokens > 0 {
				totalIn += msg.Message.Usage.InputTokens
			}
			if msg.Message.Usage.OutputTokens > 0 {
				totalOut += msg.Message.Usage.OutputTokens
			}
		case msg.Type == "result":
			if msg.Result != "" {
				lastText = msg.Result
			}

		// Codex format: item.completed with nested item object.
		case msg.Type == "item.completed" && msg.Item != nil:
			if msg.Item.Type == "agent_message" && msg.Item.Text != "" {
				lastText = msg.Item.Text
			}

		// Codex format: turn.completed with usage stats.
		case msg.Type == "turn.completed" && msg.Usage != nil:
			totalIn += msg.Usage.InputTokens
			totalOut += msg.Usage.OutputTokens
		}
	}

	if err := cmd.Wait(); err != nil {
		return Response{}, fmt.Errorf("%s exited with error: %w", p.name, err)
	}

	resp.Text = lastText
	resp.InputTokens = totalIn
	resp.OutputTokens = totalOut
	resp.DurationMS = int(time.Since(start).Milliseconds())

	// Try to detect commit SHA from git.
	resp.CommitSHA = detectLatestCommit(ctx, workDir)

	return resp, nil
}

func (p *CLIProvider) buildArgs(prompt, jsonlFile string) []string {
	switch p.name {
	case "claude":
		return []string{
			"--print",
			"--output-format", "stream-json",
			"--max-turns", "50",
			"--dangerously-skip-permissions",
			"--prompt", prompt,
		}
	case "codex":
		return []string{
			"exec",
			"--full-auto",
			"--json",
			prompt,
		}
	default:
		return []string{prompt}
	}
}

func detectLatestCommit(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// JSONL message types — supports both Claude and Codex formats.

type jsonlMessage struct {
	Type string `json:"type"`

	// Claude format fields.
	Message jsonlAssist `json:"message,omitempty"`
	Result  string      `json:"result,omitempty"`

	// Codex format fields.
	Item  *jsonlItem  `json:"item,omitempty"`
	Usage *jsonlUsage `json:"usage,omitempty"`
}

type jsonlAssist struct {
	Content []jsonlBlock `json:"content,omitempty"`
	Usage   jsonlUsage   `json:"usage,omitempty"`
}

type jsonlBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type jsonlItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type jsonlUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
