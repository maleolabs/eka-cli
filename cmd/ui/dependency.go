package ui

import "fmt"

// DependencyTree is the hierarchy presentation primitive: a root line
// followed by recursive child nodes using the classic box-drawing tree
// connectors (├── / └── / │). Every node carries a type icon and text
// colored as one span, plus an optional edge annotation rendered dim
// after the text (e.g. "derives-from arc:feather-system"). Rendering
// is a pure function of the added nodes, so output is deterministic on
// TTY and non-TTY alike.
//
// The primitive knows nothing about domain data: icons, text and edge
// annotations are rendered exactly as given. Non-TTY output is plain
// UTF-8.
type DependencyTree struct {
	s         *Style
	root      string
	rootColor func(string) string
	nodes     []*DepNode
}

// DepNode is one node of a DependencyTree. Obtain it from Add and
// extend it with child nodes and an edge annotation.
type DepNode struct {
	s     *Style
	icon  string
	text  string
	color func(string) string
	edge  string
	kids  []*DepNode
}

// NewDependencyTree creates a tree rooted at root ("" = no root line).
// rootColor colors the root line (nil = accent color).
func NewDependencyTree(s *Style, root string, rootColor func(string) string) *DependencyTree {
	return &DependencyTree{s: s, root: root, rootColor: rootColor}
}

// Add appends a child node to the root and returns its handle.
func (t *DependencyTree) Add(icon, text string, color func(string) string) *DepNode {
	n := &DepNode{s: t.s, icon: icon, text: text, color: color}
	t.nodes = append(t.nodes, n)
	return n
}

// Add appends a child node to n and returns its handle.
func (n *DepNode) Add(icon, text string, color func(string) string) *DepNode {
	k := &DepNode{s: n.s, icon: icon, text: text, color: color}
	n.kids = append(n.kids, k)
	return k
}

// Edge sets the node's edge annotation, rendered dim after the text.
func (n *DepNode) Edge(edge string) *DepNode {
	n.edge = edge
	return n
}

// Render prints the root line (if any) followed by the tree. A tree
// without nodes prints only the root line.
func (t *DependencyTree) Render() {
	s := t.s
	if t.root != "" {
		if t.rootColor != nil {
			fmt.Fprintln(s.W, t.rootColor(t.root))
		} else {
			fmt.Fprintln(s.W, s.Accent(t.root))
		}
	}
	for i, n := range t.nodes {
		t.renderNode(n, "", i == len(t.nodes)-1)
	}
}

// renderNode prints one node line and recurses into its children. The
// last child of a level uses └──, the others ├──; child prefixes keep
// the │ vertical connector for non-last branches. Connectors are dim
// (structural, like the board and card borders); the node text keeps
// its semantic color.
func (t *DependencyTree) renderNode(n *DepNode, prefix string, last bool) {
	s := t.s
	conn := TreeBranch
	if last {
		conn = TreeLast
	}
	fmt.Fprintln(s.W, s.paint(ColorDim, prefix+conn+" ")+n.line())
	childPrefix := prefix + TreeVert + "  "
	if last {
		childPrefix = prefix + TreeSpace
	}
	for i, k := range n.kids {
		t.renderNode(k, childPrefix, i == len(n.kids)-1)
	}
}

// line renders the node: icon + colored text, with the dim edge
// annotation appended when set.
func (n *DepNode) line() string {
	text := n.text
	if n.color != nil {
		text = n.color(text)
	}
	line := text
	if n.icon != "" {
		line = n.icon + " " + line
	}
	if n.edge != "" {
		line += " " + n.s.Dim("("+n.edge+")")
	}
	return line
}
