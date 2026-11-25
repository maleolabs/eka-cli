package cmd

import (
	"testing"
)

func TestResolveProjectSES_TwoProjects(t *testing.T) {
	want := "project-local only"
	// Implement targeted isolation: runtime fixture with two registered
	// repos mapped to distinct project IDs/namespaces and two SES units.
	// Verify only the current repo’s SES is resolved.
	t.Skip("placeholder")
	_ = want
}
