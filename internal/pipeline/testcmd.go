package pipeline

import (
	"fmt"
	"path/filepath"
	"strings"
)

func parseTestCommand(cmd string) ([]string, error) {
	var args []string
	var token strings.Builder
	tokenStarted := false
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	flush := func() {
		if !tokenStarted {
			return
		}
		args = append(args, token.String())
		token.Reset()
		tokenStarted = false
	}

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]

		if escaped {
			token.WriteByte(ch)
			tokenStarted = true
			escaped = false
			continue
		}

		if !inSingleQuote && !inDoubleQuote {
			if reason, unsafe := unsafeTestCommandConstruct(cmd, i); unsafe {
				return nil, fmt.Errorf("invalid test_cmd: %s", reason)
			}
		}

		switch {
		case inSingleQuote:
			switch ch {
			case '\'':
				inSingleQuote = false
			default:
				token.WriteByte(ch)
				tokenStarted = true
			}
		case inDoubleQuote:
			switch ch {
			case '"':
				inDoubleQuote = false
			case '\\':
				tokenStarted = true
				escaped = true
			default:
				token.WriteByte(ch)
				tokenStarted = true
			}
		default:
			switch ch {
			case ' ', '\t', '\n', '\r', '\f', '\v':
				flush()
			case '\'':
				tokenStarted = true
				inSingleQuote = true
			case '"':
				tokenStarted = true
				inDoubleQuote = true
			case '\\':
				tokenStarted = true
				escaped = true
			default:
				token.WriteByte(ch)
				tokenStarted = true
			}
		}
	}

	if escaped {
		return nil, fmt.Errorf("invalid test_cmd: trailing escape")
	}
	if inSingleQuote || inDoubleQuote {
		return nil, fmt.Errorf("invalid test_cmd: unterminated quote")
	}

	flush()
	if len(args) == 0 {
		return nil, fmt.Errorf("invalid test_cmd: empty command")
	}
	return args, nil
}

func unsafeTestCommandConstruct(cmd string, i int) (string, bool) {
	ch := cmd[i]
	switch ch {
	case ';':
		return "disallowed token ';'", true
	case '|':
		if i+1 < len(cmd) && cmd[i+1] == '|' {
			return "disallowed token '||'", true
		}
		return "disallowed token '|'", true
	case '&':
		if i+1 < len(cmd) && cmd[i+1] == '&' {
			return "disallowed token '&&'", true
		}
		return "disallowed token '&'", true
	case '<':
		return "disallowed token '<'", true
	case '>':
		return "disallowed token '>'", true
	case '`':
		return "disallowed token '`'", true
	case '$':
		if i+1 < len(cmd) && cmd[i+1] == '(' {
			return "disallowed token '$('", true
		}
		return "disallowed token '$'", true
	}
	return "", false
}

var disallowedTestCommandExecutables = map[string]struct{}{
	"sh":         {},
	"bash":       {},
	"zsh":        {},
	"dash":       {},
	"ksh":        {},
	"csh":        {},
	"tcsh":       {},
	"fish":       {},
	"cmd":        {},
	"powershell": {},
	"pwsh":       {},
}

func validateTestCommandArgs(args []string) error {
	base := normalizeExecutableName(args[0])
	if _, disallowed := disallowedTestCommandExecutables[base]; disallowed {
		return fmt.Errorf("invalid test_cmd: disallowed executable %q", base)
	}
	if base == "env" {
		for i := firstEnvCommandIndex(args); i < len(args); i++ {
			next := normalizeExecutableName(args[i])
			if _, disallowed := disallowedTestCommandExecutables[next]; disallowed {
				return fmt.Errorf("invalid test_cmd: disallowed executable %q", next)
			}
			break
		}
	}
	if base == "busybox" && len(args) > 1 {
		next := normalizeExecutableName(args[1])
		if _, disallowed := disallowedTestCommandExecutables[next]; disallowed {
			return fmt.Errorf("invalid test_cmd: disallowed executable %q", next)
		}
	}
	return nil
}

func normalizeExecutableName(execName string) string {
	base := strings.ToLower(filepath.Base(execName))
	return strings.TrimSuffix(base, ".exe")
}

func firstEnvCommandIndex(args []string) int {
	i := 1
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			i++
			break
		}
		if strings.Contains(arg, "=") {
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		if arg == "-u" {
			i += 2
			continue
		}
		i++
	}
	return i
}
