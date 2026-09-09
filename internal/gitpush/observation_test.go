package gitpush

import (
	"strings"
	"testing"
)

func TestAdvertisementRequiresExactUnambiguousBranch(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			plan := fixturePlan(t, format)
			line := plan.Candidate.Head + "\t" + plan.TargetRef + "\n"
			observed, err := ParseAdvertisement(plan, []byte(line))
			if err != nil || observed.Commit == nil || *observed.Commit != plan.Candidate.Head || observed.TargetRef != plan.TargetRef {
				t.Fatal("exact advertisement rejected", err)
			}
			absent, err := ParseAdvertisement(plan, nil)
			if err != nil || absent.Commit != nil || absent.TargetRef != plan.TargetRef {
				t.Fatal("absence observation confused with failure", err)
			}
			for _, bad := range []string{
				strings.TrimSuffix(line, "\n"), line + line,
				strings.Replace(line, plan.TargetRef, "refs/heads/other", 1),
				strings.Replace(line, "\t", " ", 1),
				strings.Replace(line, plan.Candidate.Head, strings.Repeat("0", len(plan.Candidate.Head)), 1),
				strings.Replace(line, plan.Candidate.Head, strings.ToUpper(plan.Candidate.Head), 1),
				"ref: refs/heads/main\tHEAD\n", strings.Repeat("x", 2049),
			} {
				if _, err := ParseAdvertisement(plan, []byte(bad)); err == nil {
					t.Fatal("ambiguous advertisement admitted")
				}
			}
		})
	}
}
