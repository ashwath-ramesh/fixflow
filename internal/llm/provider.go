package llm

import "context"

// Provider is the interface for LLM backends.
type Provider interface {
	// Name returns the provider name (e.g. "claude", "codex").
	Name() string

	// Run invokes the LLM CLI tool in the given workDir with the given prompt.
	// jsonlPath is the pre-determined path for the JSONL session file.
	// Returns the response with token counts and timing.
	Run(ctx context.Context, workDir, prompt, jsonlPath string) (Response, error)
}

// Response captures the output of an LLM invocation.
type Response struct {
	Text         string
	InputTokens  int
	OutputTokens int
	DurationMS   int
	JSONLPath    string
	CommitSHA    string // Set if the LLM tool committed changes.
}
