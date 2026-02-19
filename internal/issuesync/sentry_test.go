package issuesync

import "testing"

func TestSentryIssueQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		assignedTeam string
		want         string
	}{
		{
			name: "no team returns unresolved only",
			want: "is:unresolved",
		},
		{
			name:         "team adds assigned filter",
			assignedTeam: "autopr",
			want:         "is:unresolved assigned:#autopr",
		},
		{
			name:         "whitespace-only team is ignored",
			assignedTeam: "  ",
			want:         "is:unresolved",
		},
		{
			name:         "team is trimmed",
			assignedTeam: "  my-team  ",
			want:         "is:unresolved assigned:#my-team",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sentryIssueQuery(tc.assignedTeam)
			if got != tc.want {
				t.Fatalf("sentryIssueQuery(%q): want %q, got %q", tc.assignedTeam, tc.want, got)
			}
		})
	}
}

