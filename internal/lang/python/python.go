// Package python implements the Python LanguageProvider for code-graft.
package python

import (
	"fmt"
	"strings"

	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// Provider implements lang.LanguageProvider for Python.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new Python LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_python.Language()),
	}
}

// Name returns "python".
func (p *Provider) Name() string {
	return "python"
}

// Extensions returns [".py"].
func (p *Provider) Extensions() []string {
	return []string{".py"}
}

// Language returns the Python Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers returns expression wrappers for Python snippets.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return nil
}

// AnalyzeScope implements rewrite.LanguageSupport.
func (p *Provider) AnalyzeScope(doc *rewrite.Document) rewrite.ScopeVerifier {
	return p.AnalyzeScopeInfo(doc)
}

// AnalyzeScopeInfo extracts Python `import foo`, `import foo as bar`, and `from foo import Bar`
// declarations, plus local assignments like `client = Client(...)` or `client = foo.Client(...)`.
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	scope := lang.NewScopeInfo()
	root := doc.Tree.RootNode()

	// Pass 1: Extract import_statement and import_from_statement
	walk(root, func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "import_statement":
			for i := uint(0); i < n.NamedChildCount(); i++ {
				c := n.NamedChild(i)
				switch c.Kind() {
				case "dotted_name":
					modName := c.Utf8Text(doc.Source)
					scope.Imports[modName] = modName
				case "aliased_import":
					nameNode := c.ChildByFieldName("name")
					aliasNode := c.ChildByFieldName("alias")
					if nameNode != nil && aliasNode != nil {
						scope.Imports[aliasNode.Utf8Text(doc.Source)] = nameNode.Utf8Text(doc.Source)
					}
				}
			}
		case "import_from_statement":
			modNode := n.ChildByFieldName("module_name")
			if modNode == nil {
				return
			}
			modName := modNode.Utf8Text(doc.Source)
			for i := uint(0); i < n.NamedChildCount(); i++ {
				c := n.NamedChild(i)
				if c.Equals(*modNode) {
					continue
				}
				switch c.Kind() {
				case "dotted_name":
					scope.Imports[c.Utf8Text(doc.Source)] = modName
				case "aliased_import":
					aliasNode := c.ChildByFieldName("alias")
					if aliasNode != nil {
						scope.Imports[aliasNode.Utf8Text(doc.Source)] = modName
					}
				}
			}
		}
	})

	// Pass 2: Track `assignment` nodes (`client = Client(...)` or `client = sdk.Client(...)`)
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "assignment" {
			return
		}
		left := n.ChildByFieldName("left")
		right := n.ChildByFieldName("right")
		if left == nil || right == nil {
			return
		}
		if left.Kind() == "identifier" {
			if pkg := inferPyExprOrigin(right, doc.Source, scope); pkg != "" {
				scope.VarOrigins[left.Utf8Text(doc.Source)] = pkg
			}
		}
	})

	return scope
}

func inferPyExprOrigin(expr *tree_sitter.Node, src []byte, scope *lang.ScopeInfo) string {
	if expr == nil {
		return ""
	}
	if expr.Kind() == "call" {
		fn := expr.ChildByFieldName("function")
		if fn != nil {
			switch fn.Kind() {
			case "identifier":
				if pkg, ok := scope.Imports[fn.Utf8Text(src)]; ok {
					return pkg
				}
			case "attribute":
				obj := fn.ChildByFieldName("object")
				if obj != nil {
					objText := obj.Utf8Text(src)
					if pkg, ok := scope.Imports[objText]; ok {
						return pkg
					}
					if pkg, ok := scope.VarOrigins[objText]; ok {
						return pkg
					}
				}
			}
		}
	}
	return ""
}

// AddImport inserts an `import <module>` or `from <module> import <symbol>` into a Python file.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, symbolOrAlias string) *rewrite.Edit {
	scope := p.AnalyzeScopeInfo(doc)
	for sym, existingMod := range scope.Imports {
		if existingMod == modulePath && (symbolOrAlias == "" || sym == symbolOrAlias) {
			return nil
		}
	}

	var importStmt string
	if symbolOrAlias == "" {
		importStmt = fmt.Sprintf("import %s", modulePath)
	} else {
		importStmt = fmt.Sprintf("from %s import %s", modulePath, symbolOrAlias)
	}

	root := doc.Tree.RootNode()
	var lastImport *tree_sitter.Node
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() == "import_statement" || c.Kind() == "import_from_statement" {
			lastImport = c
		}
	}

	if lastImport != nil {
		existing := lastImport.Utf8Text(doc.Source)
		newText := fmt.Sprintf("%s\n%s", existing, importStmt)
		edit := doc.NodeEdit(lastImport, []byte(newText), "add Python import "+modulePath)
		return &edit
	}

	return &rewrite.Edit{
		StartByte:   0,
		EndByte:     0,
		Replacement: []byte(importStmt + "\n"),
		Description: "add Python import " + modulePath,
	}
}

// RemoveImport removes an `import <modulePath>` or `from <modulePath> import ...` from a Python file.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	root := doc.Tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		switch c.Kind() {
		case "import_statement":
			text := strings.TrimSpace(c.Utf8Text(doc.Source))
			if text == "import "+modulePath || strings.HasPrefix(text, "import "+modulePath+" as ") {
				startB, endB := expandToFullLine(doc.Source, c.StartByte(), c.EndByte())
				return &rewrite.Edit{
					StartByte:   startB,
					EndByte:     endB,
					Replacement: nil,
					Description: "remove Python import " + modulePath,
				}
			}
		case "import_from_statement":
			modNode := c.ChildByFieldName("module_name")
			if modNode != nil && modNode.Utf8Text(doc.Source) == modulePath {
				startB, endB := expandToFullLine(doc.Source, c.StartByte(), c.EndByte())
				return &rewrite.Edit{
					StartByte:   startB,
					EndByte:     endB,
					Replacement: nil,
					Description: "remove Python import " + modulePath,
				}
			}
		}
	}
	return nil
}

func expandToFullLine(src []byte, startB, endB uint) (uint, uint) {
	for startB > 0 && (src[startB-1] == ' ' || src[startB-1] == '\t') {
		startB--
	}
	if int(endB) < len(src) && src[endB] == '\r' {
		endB++
	}
	if int(endB) < len(src) && src[endB] == '\n' {
		endB++
	}
	return startB, endB
}

func walk(n *tree_sitter.Node, fn func(*tree_sitter.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for i := uint(0); i < n.ChildCount(); i++ {
		walk(n.Child(i), fn)
	}
}
