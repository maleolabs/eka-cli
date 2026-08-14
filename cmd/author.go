package cmd

import (
	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the shared author-identity presentation helpers
// (RFC author identity: user | agent | worker). The identity kind is
// rendered as a colored tag — user neutral, agent progress, worker
// warning — so the reader sees at a glance WHO authored or authorized
// a document, note or change-log entry.

// authorLabel renders one author identity for display: the name plus
// the kind tag for non-user identities ("agent-x [agent]").
func authorLabel(s *ui.Style, a conformance.AuthorIdentity) string {
	if a.Name == "" {
		return ""
	}
	if a.IsUser() {
		return a.Name
	}
	return a.Name + " " + authorKindTag(s, a.Kind)
}

// authorKindTag renders the colored kind tag of a non-user identity.
func authorKindTag(s *ui.Style, kind string) string {
	switch kind {
	case conformance.KindAgent:
		return s.Progress("[" + kind + "]")
	case conformance.KindWorker:
		return s.Warning("[" + kind + "]")
	default:
		return s.Dim("[" + kind + "]")
	}
}
