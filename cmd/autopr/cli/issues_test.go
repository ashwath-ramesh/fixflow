package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunIssuesMutuallyExclusiveFlags(t *testing.T) {
	prevEligible := issuesEligible
	prevIneligible := issuesIneligible
	defer func() {
		issuesEligible = prevEligible
		issuesIneligible = prevIneligible
	}()

	issuesEligible = true
	issuesIneligible = true

	err := runIssues(&cobra.Command{}, nil)
	if err == nil {
		t.Fatalf("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
	}
}
