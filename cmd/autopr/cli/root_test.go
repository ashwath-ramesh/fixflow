package cli

import (
	"fmt"
	"testing"
)

func TestRootCmdVersionIncludesCommitAndDate(t *testing.T) {
	want := fmt.Sprintf("%s (%s, %s)", version, commit, date)
	if got := rootCmd.Version; got != want {
		t.Fatalf("rootCmd.Version = %q, want %q", got, want)
	}
}
