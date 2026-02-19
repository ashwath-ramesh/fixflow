package pipeline

import (
	"os"
	"regexp"
	"strings"
)

// maxPromptLen is the maximum length of issue body included in prompts.
const maxPromptLen = 50000

// LoadTemplate reads a prompt template from disk.
// Returns empty string if path is empty or file doesn't exist.
func LoadTemplate(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SanitizeIssueContent prepares issue text for safe LLM inclusion.
// Strips HTML, truncates, and wraps in delimiters.
func SanitizeIssueContent(s string) string {
	s = stripHTML(s)
	s = strings.TrimSpace(s)
	s = neutralizeLLMDirectives(s)
	if len(s) > maxPromptLen {
		s = s[:maxPromptLen] + "\n... (truncated)"
	}
	return s
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)
var promptInstructionRe = regexp.MustCompile(`(?im)(?i)^\s*(ignore|disregard|override|act as|do not|you are|system|assistant|developer|user)\b`)

func stripHTML(s string) string {
	return htmlTagRe.ReplaceAllString(s, "")
}

func neutralizeLLMDirectives(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if promptInstructionRe.MatchString(line) {
			lines[i] = "> " + strings.TrimSpace(line)
		}
	}
	return strings.Join(lines, "\n")
}

// BuildPrompt constructs a prompt from a template and variables.
// Variables are replaced as {{key}} placeholders.
func BuildPrompt(template string, vars map[string]string) string {
	result := template
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}
