package ui

import (
	"strings"
	"testing"
)

func TestDependencyTreeLayout(t *testing.T) {
	s, buf := testStyle()
	tree := NewDependencyTree(s, "root", nil)
	dec := tree.Add("", "Decisions", s.Info)
	dec.Add("✓", "feather/adr:one", nil).Edge("derives-from req:core")
	dec.Add("○", "feather/adr:two", nil)
	tree.Add("", "Vocabulary", s.Info).Add("✓", "feather/gls:terms", nil)
	tree.Render()
	want := "" +
		"root\n" +
		"├── Decisions\n" +
		"│  ├── ✓ feather/adr:one (derives-from req:core)\n" +
		"│  └── ○ feather/adr:two\n" +
		"└── Vocabulary\n" +
		"   └── ✓ feather/gls:terms\n"
	if got := stripMargin(buf.String()); got != want {
		t.Errorf("tree layout mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestDependencyTreeNoRootLine(t *testing.T) {
	s, buf := testStyle()
	tree := NewDependencyTree(s, "", nil)
	tree.Add("•", "node", nil)
	tree.Render()
	if strings.Contains(buf.String(), "root") || !strings.Contains(buf.String(), "node") {
		t.Errorf("empty root must print no root line:\n%s", buf.String())
	}
}

func TestDependencyTreeEdgeAnnotation(t *testing.T) {
	s, buf := testStyle()
	tree := NewDependencyTree(s, "root", nil)
	tree.Add("✓", "feather/adr:one", nil).Edge("derives-from arc:feather-system")
	tree.Render()
	if !strings.Contains(buf.String(), "(derives-from arc:feather-system)") {
		t.Errorf("edge annotation must render after the node text:\n%s", buf.String())
	}
}

func TestDependencyTreeDeterministic(t *testing.T) {
	build := func() string {
		s, buf := testStyle()
		tree := NewDependencyTree(s, "root", nil)
		n := tree.Add("", "group", nil)
		n.Add("✓", "a", nil)
		tree.Add("", "g2", nil)
		tree.Render()
		return buf.String()
	}
	if build() != build() {
		t.Error("tree output must be deterministic")
	}
}
