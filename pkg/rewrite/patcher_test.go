package rewrite_test

import (
	"testing"

	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

func TestDocumentApplyEditsAndIncrementalReparse(t *testing.T) {
	goLang := tree_sitter.NewLanguage(tree_sitter_go.Language())

	tests := []struct {
		name           string
		initialSource  string
		buildEdits     func(doc *rewrite.Document) []rewrite.Edit
		expectedSource string
		expectedApplied int
	}{
		{
			name: "multiple edits in reverse order preserving comments and formatting",
			initialSource: `package main

// Leading comment must stay intact
func main() {
	a := oldCall(1) // inline comment 1
	b := oldCall(2) // inline comment 2
}
`,
			buildEdits: func(doc *rewrite.Document) []rewrite.Edit {
				var edits []rewrite.Edit
				walkNodes(doc.Tree.RootNode(), func(n *tree_sitter.Node) {
					if n.Kind() == "identifier" && n.Utf8Text(doc.Source) == "oldCall" {
						edits = append(edits, doc.NodeEdit(n, []byte("newCall"), "rename oldCall"))
					}
				})
				return edits
			},
			expectedSource: `package main

// Leading comment must stay intact
func main() {
	a := newCall(1) // inline comment 1
	b := newCall(2) // inline comment 2
}
`,
			expectedApplied: 2,
		},
		{
			name: "multi-line replacement aligns indentation with target line",
			initialSource: `package main

func run() {
	if true {
		doLegacy()
	}
}
`,
			buildEdits: func(doc *rewrite.Document) []rewrite.Edit {
				var edits []rewrite.Edit
				walkNodes(doc.Tree.RootNode(), func(n *tree_sitter.Node) {
					if n.Kind() == "call_expression" && n.Utf8Text(doc.Source) == "doLegacy()" {
						edits = append(edits, doc.NodeEdit(n, []byte("setup()\ndoModern()"), "expand legacy call"))
					}
				})
				return edits
			},
			expectedSource: `package main

func run() {
	if true {
		setup()
		doModern()
	}
}
`,
			expectedApplied: 1,
		},
		{
			name: "overlapping edits are deduplicated safely",
			initialSource: `package main

func main() {
	outer(inner(x))
}
`,
			buildEdits: func(doc *rewrite.Document) []rewrite.Edit {
				var edits []rewrite.Edit
				walkNodes(doc.Tree.RootNode(), func(n *tree_sitter.Node) {
					if n.Kind() == "call_expression" {
						if n.Utf8Text(doc.Source) == "outer(inner(x))" {
							edits = append(edits, doc.NodeEdit(n, []byte("replacedOuter(x)"), "outer"))
						} else if n.Utf8Text(doc.Source) == "inner(x)" {
							edits = append(edits, doc.NodeEdit(n, []byte("replacedInner(x)"), "inner"))
						}
					}
				})
				return edits
			},
			expectedSource: `package main

func main() {
	replacedOuter(x)
}
`,
			expectedApplied: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := rewrite.NewDocument("main.go", "go", goLang, []byte(tc.initialSource))
			require.NoError(t, err)
			defer doc.Close()

			edits := tc.buildEdits(doc)
			applied, err := doc.ApplyEdits(edits)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedApplied, applied)
			assert.Equal(t, tc.expectedSource, string(doc.Source))
			assert.False(t, doc.Tree.RootNode().HasError(), "re-parsed CST should not have syntax errors")
		})
	}
}

func walkNodes(node *tree_sitter.Node, visit func(n *tree_sitter.Node)) {
	if node == nil {
		return
	}
	visit(node)
	for i := uint(0); i < node.ChildCount(); i++ {
		walkNodes(node.Child(i), visit)
	}
}
