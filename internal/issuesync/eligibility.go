package issuesync

import (
	"strings"
	"time"
)

type issueEligibility struct {
	Eligible    bool
	SkipReason  string
	EvaluatedAt string
}

func evaluateGitHubIssueEligibility(includeLabels, issueLabels []string, evaluatedAt time.Time) issueEligibility {
	required := normalizeLabelSet(includeLabels)
	if evaluatedAt.IsZero() {
		evaluatedAt = time.Now().UTC()
	}
	result := issueEligibility{
		Eligible:    true,
		EvaluatedAt: evaluatedAt.UTC().Format(time.RFC3339),
	}
	if len(required) == 0 {
		return result
	}

	issueSet := make(map[string]struct{}, len(issueLabels))
	for _, label := range issueLabels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if normalized == "" {
			continue
		}
		issueSet[normalized] = struct{}{}
	}
	for _, requiredLabel := range required {
		if _, ok := issueSet[requiredLabel]; ok {
			return result
		}
	}

	result.Eligible = false
	result.SkipReason = "missing required labels: " + strings.Join(required, ", ")
	return result
}

func normalizeLabelSet(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}
