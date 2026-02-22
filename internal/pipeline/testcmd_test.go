package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"autopr/internal/config"
)

func TestParseTestCommandSimple(t *testing.T) {
	t.Parallel()

	got, err := parseTestCommand("go test ./...")
	if err != nil {
		t.Fatalf("parseTestCommand returned error: %v", err)
	}
	want := []string{"go", "test", "./..."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got=%v want=%v", got, want)
	}
}

func TestParseTestCommandQuotedArg(t *testing.T) {
	t.Parallel()

	got, err := parseTestCommand(`go test -run "Test Foo"`)
	if err != nil {
		t.Fatalf("parseTestCommand returned error: %v", err)
	}
	want := []string{"go", "test", "-run", "Test Foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got=%v want=%v", got, want)
	}
}

func TestParseTestCommandSingleQuotedArg(t *testing.T) {
	t.Parallel()

	got, err := parseTestCommand("go test -run 'Test Foo'")
	if err != nil {
		t.Fatalf("parseTestCommand returned error: %v", err)
	}
	want := []string{"go", "test", "-run", "Test Foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got=%v want=%v", got, want)
	}
}

func TestParseTestCommandEscapedWhitespace(t *testing.T) {
	t.Parallel()

	got, err := parseTestCommand(`go test -run Test\ Foo`)
	if err != nil {
		t.Fatalf("parseTestCommand returned error: %v", err)
	}
	want := []string{"go", "test", "-run", "Test Foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got=%v want=%v", got, want)
	}
}

func TestParseTestCommandMixedQuoting(t *testing.T) {
	t.Parallel()

	got, err := parseTestCommand(`go test -run "Test 'Foo'"`)
	if err != nil {
		t.Fatalf("parseTestCommand returned error: %v", err)
	}
	want := []string{"go", "test", "-run", "Test 'Foo'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got=%v want=%v", got, want)
	}
}

func TestParseTestCommandEmptyQuotedArg(t *testing.T) {
	t.Parallel()

	got, err := parseTestCommand(`go test -run ""`)
	if err != nil {
		t.Fatalf("parseTestCommand returned error: %v", err)
	}
	want := []string{"go", "test", "-run", ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got=%v want=%v", got, want)
	}
}

func TestParseTestCommandAllowsUnsafeTextInsideQuotes(t *testing.T) {
	t.Parallel()

	got, err := parseTestCommand(`go test -run "A && B; C | D $(echo x)"`)
	if err != nil {
		t.Fatalf("parseTestCommand returned error: %v", err)
	}
	want := []string{"go", "test", "-run", "A && B; C | D $(echo x)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got=%v want=%v", got, want)
	}
}

func TestParseTestCommandRejectsUnsafeConstructs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cmd        string
		wantReason string
	}{
		{name: "and and", cmd: "go test && echo bad", wantReason: "'&&'"},
		{name: "semicolon", cmd: "go test; echo bad", wantReason: "';'"},
		{name: "backticks", cmd: "go test `echo bad`", wantReason: "'`'"},
		{name: "subshell", cmd: "go test $(echo bad)", wantReason: "'$('"},
		{name: "pipe", cmd: "go test | cat", wantReason: "'|'"},
		{name: "or or", cmd: "go test || echo bad", wantReason: "'||'"},
		{name: "ampersand", cmd: "go test & cat", wantReason: "'&'"},
		{name: "input redirect", cmd: "go test < file", wantReason: "'<'"},
		{name: "output redirect", cmd: "go test > file", wantReason: "'>'"},
		{name: "bare dollar", cmd: "go test $TEST_DB", wantReason: "'$'"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseTestCommand(tc.cmd)
			if err == nil {
				t.Fatalf("expected parse error for %q", tc.cmd)
			}
			if !strings.Contains(err.Error(), "invalid test_cmd") {
				t.Fatalf("expected invalid test_cmd prefix, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("expected error to include %s, got: %v", tc.wantReason, err)
			}
		})
	}
}

func TestParseTestCommandRejectsMalformedSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cmd        string
		wantReason string
	}{
		{name: "unterminated single quote", cmd: "go test -run 'Test Foo", wantReason: "unterminated quote"},
		{name: "unterminated double quote", cmd: `go test -run "Test Foo`, wantReason: "unterminated quote"},
		{name: "trailing escape", cmd: `go test \`, wantReason: "trailing escape"},
		{name: "whitespace only", cmd: "   ", wantReason: "empty command"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseTestCommand(tc.cmd)
			if err == nil {
				t.Fatalf("expected parse error for %q", tc.cmd)
			}
			if !strings.Contains(err.Error(), "invalid test_cmd") {
				t.Fatalf("expected invalid test_cmd prefix, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("expected error to include %q, got: %v", tc.wantReason, err)
			}
		})
	}
}

func TestRunTestCommandExecutesWithoutShell(t *testing.T) {
	t.Parallel()

	output, err := runTestCommand(context.Background(), t.TempDir(), "go version")
	if err != nil {
		t.Fatalf("runTestCommand returned error: %v", err)
	}
	if !strings.Contains(output, "go version") {
		t.Fatalf("expected go version output, got: %q", output)
	}
}

func TestRunTestCommandRejectsUnsafeCommand(t *testing.T) {
	t.Parallel()

	output, err := runTestCommand(context.Background(), t.TempDir(), "go version && echo bad")
	if err == nil {
		t.Fatal("expected runTestCommand error")
	}
	if !strings.Contains(err.Error(), "invalid test_cmd") {
		t.Fatalf("expected invalid test_cmd error, got: %v", err)
	}
	if !strings.Contains(output, "invalid test_cmd") {
		t.Fatalf("expected validation output, got: %q", output)
	}
}

func TestRunTestCommandRejectsShellExecutable(t *testing.T) {
	t.Parallel()

	output, err := runTestCommand(context.Background(), t.TempDir(), "sh -c 'echo hi'")
	if err == nil {
		t.Fatal("expected runTestCommand error")
	}
	if !strings.Contains(err.Error(), "invalid test_cmd: disallowed executable") {
		t.Fatalf("expected disallowed executable error, got: %v", err)
	}
	if !strings.Contains(output, "invalid test_cmd: disallowed executable") {
		t.Fatalf("expected validation output, got: %q", output)
	}
}

func TestRunTestCommandRejectsShellViaEnv(t *testing.T) {
	t.Parallel()

	output, err := runTestCommand(context.Background(), t.TempDir(), "env sh -c 'echo hi'")
	if err == nil {
		t.Fatal("expected runTestCommand error")
	}
	if !strings.Contains(err.Error(), "invalid test_cmd: disallowed executable") {
		t.Fatalf("expected disallowed executable error, got: %v", err)
	}
	if !strings.Contains(output, "invalid test_cmd: disallowed executable") {
		t.Fatalf("expected validation output, got: %q", output)
	}
}

func TestRunTestCommandRejectsShellViaEnvAssignment(t *testing.T) {
	t.Parallel()

	output, err := runTestCommand(context.Background(), t.TempDir(), "env FOO=bar sh -c 'echo hi'")
	if err == nil {
		t.Fatal("expected runTestCommand error")
	}
	if !strings.Contains(err.Error(), "invalid test_cmd: disallowed executable") {
		t.Fatalf("expected disallowed executable error, got: %v", err)
	}
	if !strings.Contains(output, "invalid test_cmd: disallowed executable") {
		t.Fatalf("expected validation output, got: %q", output)
	}
}

func TestRunTestCommandRejectsShellViaBusybox(t *testing.T) {
	t.Parallel()

	output, err := runTestCommand(context.Background(), t.TempDir(), "busybox sh -c 'echo hi'")
	if err == nil {
		t.Fatal("expected runTestCommand error")
	}
	if !strings.Contains(err.Error(), "invalid test_cmd: disallowed executable") {
		t.Fatalf("expected disallowed executable error, got: %v", err)
	}
	if !strings.Contains(output, "invalid test_cmd: disallowed executable") {
		t.Fatalf("expected validation output, got: %q", output)
	}
}

func TestRunTestsStoresValidationErrorArtifact(t *testing.T) {
	t.Parallel()

	runner, store, issue, jobID := setupRunStepsJob(t, nil, "testing")
	ctx := context.Background()
	projectCfg := &config.ProjectConfig{
		Name:       "project",
		RepoURL:    "https://example.com/org/repo.git",
		BaseBranch: "main",
		TestCmd:    "go version && echo bad",
	}

	err := runner.runTests(ctx, jobID, issue, projectCfg, t.TempDir())
	if !errors.Is(err, errTestsFailed) {
		t.Fatalf("expected errTestsFailed, got: %v", err)
	}

	artifact, err := store.GetLatestArtifact(ctx, jobID, "test_output")
	if err != nil {
		t.Fatalf("expected test_output artifact, got err: %v", err)
	}
	if !strings.Contains(artifact.Content, "invalid test_cmd") {
		t.Fatalf("expected artifact to include validation error, got: %q", artifact.Content)
	}
}
