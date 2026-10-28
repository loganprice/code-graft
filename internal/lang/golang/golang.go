// Package golang implements the Go LanguageProvider for code-graft.
package golang

import (
	"fmt"
	"path"
	"strings"

	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

// Provider implements lang.LanguageProvider for Go.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new Go LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_go.Language()),
	}
}

// Name returns "go".
func (p *Provider) Name() string {
	return "go"
}

// Extensions returns [".go"].
func (p *Provider) Extensions() []string {
	return []string{".go"}
}

// Language returns the Go Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers wraps Go expression or statement snippets inside a dummy function
// so they parse cleanly into CST subtrees.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return []rewrite.ExpressionWrapperFunc{
		// 1. Expression wrapper: `_ = <snippet>`
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			wrapped := fmt.Sprintf("package _p\nfunc _f() {\n_ = %s\n}\n", snippet)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && n.Kind() == "assignment_statement" {
						right := n.ChildByFieldName("right")
						if right != nil && right.NamedChildCount() > 0 {
							found = right.NamedChild(0)
						}
					}
				})
				return found
			}
			return wrapped, unwrap
		},
		// 2. Statement wrapper: `<snippet>` inside `func _f() { ... }`
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			wrapped := fmt.Sprintf("package _p\nfunc _f() {\n%s\n}\n", snippet)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && n.Kind() == "statement_list" && n.NamedChildCount() > 0 {
						found = n.NamedChild(0)
					}
				})
				return found
			}
			return wrapped, unwrap
		},
		// 3. Parameter declaration wrapper: `func _f(<snippet>) {}`
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			wrapped := fmt.Sprintf("package _p\nfunc _f(%s) {}\n", snippet)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && n.Kind() == "parameter_declaration" {
						found = n
					}
				})
				return found
			}
			return wrapped, unwrap
		},
	}
}

// AnalyzeScope implements rewrite.LanguageSupport.
func (p *Provider) AnalyzeScope(doc *rewrite.Document) rewrite.ScopeVerifier {
	return p.AnalyzeScopeInfo(doc)
}

// AnalyzeScopeInfo extracts Go imports (`import "pkg/foo"` or `import alias "pkg/foo"`)
// and local variable origins (`client := foo.NewClient(...)`, `var c foo.Client`).
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	scope := lang.NewScopeInfo()
	root := doc.Tree.RootNode()

	// Pass 1: Extract all import_spec nodes.
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "import_spec" {
			return
		}
		pathNode := n.ChildByFieldName("path")
		if pathNode == nil {
			return
		}
		importPath := strings.Trim(pathNode.Utf8Text(doc.Source), "\"`")
		alias := path.Base(importPath)
		if nameNode := n.ChildByFieldName("name"); nameNode != nil {
			alias = strings.TrimSpace(nameNode.Utf8Text(doc.Source))
		}
		if alias != "_" && alias != "." && importPath != "" {
			scope.Imports[alias] = importPath
		}
	})

	// Pass 2: Track short_var_declaration, assignment_statement, var_spec, and parameter_declaration.
	walk(root, func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "short_var_declaration", "assignment_statement":
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left == nil || right == nil || right.NamedChildCount() == 0 {
				return
			}
			rhsExpr := right.NamedChild(0)
			if pkgPath := inferGoExprOrigin(rhsExpr, doc.Source, scope); pkgPath != "" {
				// Bind the first identifier on the LHS (e.g., `client, err := sdk.New()`).
				if left.NamedChildCount() > 0 {
					lhsFirst := left.NamedChild(0)
					if lhsFirst.Kind() == "identifier" {
						scope.VarOrigins[lhsFirst.Utf8Text(doc.Source)] = pkgPath
					}
				}
			}
		case "var_spec", "parameter_declaration":
			typeNode := n.ChildByFieldName("type")
			if typeNode != nil {
				if pkgPath := inferGoTypeOrigin(typeNode, doc.Source, scope); pkgPath != "" {
					for i := uint(0); i < n.NamedChildCount(); i++ {
						c := n.NamedChild(i)
						if c.Kind() == "identifier" {
							scope.VarOrigins[c.Utf8Text(doc.Source)] = pkgPath
						}
					}
				}
			}
		}
	})

	return scope
}

func inferGoExprOrigin(expr *tree_sitter.Node, src []byte, scope *lang.ScopeInfo) string {
	if expr == nil {
		return ""
	}
	switch expr.Kind() {
	case "call_expression":
		fn := expr.ChildByFieldName("function")
		if fn != nil && fn.Kind() == "selector_expression" {
			op := fn.ChildByFieldName("operand")
			if op != nil {
				opText := op.Utf8Text(src)
				if pkg, ok := scope.Imports[opText]; ok {
					return pkg
				}
				if pkg, ok := scope.VarOrigins[opText]; ok {
					return pkg
				}
			}
		}
	case "unary_expression":
		// e.g. `&sdk.Client{}`
		op := expr.ChildByFieldName("operand")
		return inferGoExprOrigin(op, src, scope)
	case "composite_literal":
		tNode := expr.ChildByFieldName("type")
		return inferGoTypeOrigin(tNode, src, scope)
	}
	return ""
}

func inferGoTypeOrigin(typeNode *tree_sitter.Node, src []byte, scope *lang.ScopeInfo) string {
	if typeNode == nil {
		return ""
	}
	if typeNode.Kind() == "pointer_type" && typeNode.NamedChildCount() > 0 {
		return inferGoTypeOrigin(typeNode.NamedChild(0), src, scope)
	}
	if typeNode.Kind() == "qualified_type" {
		pkgNode := typeNode.ChildByFieldName("package")
		if pkgNode != nil {
			if pkg, ok := scope.Imports[pkgNode.Utf8Text(src)]; ok {
				return pkg
			}
		}
	}
	return ""
}

// AddImport inserts an import for modulePath (with optional alias) into a Go source file.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, alias string) *rewrite.Edit {
	scope := p.AnalyzeScopeInfo(doc)
	for _, existingPath := range scope.Imports {
		if existingPath == modulePath {
			return nil
		}
	}

	specLine := fmt.Sprintf("%q", modulePath)
	if alias != "" {
		specLine = fmt.Sprintf("%s %q", alias, modulePath)
	}

	root := doc.Tree.RootNode()
	var lastImportDecl *tree_sitter.Node
	var packageClause *tree_sitter.Node

	for i := uint(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(i)
		switch child.Kind() {
		case "package_clause":
			packageClause = child
		case "import_declaration":
			lastImportDecl = child
		}
	}

	if lastImportDecl != nil {
		// Check if it has an import_spec_list `( ... )`
		for i := uint(0); i < lastImportDecl.NamedChildCount(); i++ {
			c := lastImportDecl.NamedChild(i)
			if c.Kind() == "import_spec_list" {
				// Insert before closing ')'
				insertPos := c.EndByte() - 1
				pt := c.EndPosition()
				if pt.Column > 0 {
					pt.Column--
				}
				replacement := []byte("\t" + specLine + "\n)")
				return &rewrite.Edit{
					StartByte:   insertPos,
					EndByte:     c.EndByte(),
					StartPoint:  pt,
					EndPoint:    c.EndPosition(),
					Replacement: replacement,
					Description: "add Go import " + modulePath,
				}
			}
		}
		// Single-line `import "foo"`: append a new import line after it.
		existingText := lastImportDecl.Utf8Text(doc.Source)
		newText := fmt.Sprintf("%s\nimport %s", existingText, specLine)
		edit := doc.NodeEdit(lastImportDecl, []byte(newText), "add Go import "+modulePath)
		return &edit
	}

	if packageClause != nil {
		existingText := packageClause.Utf8Text(doc.Source)
		newText := fmt.Sprintf("%s\n\nimport %s", existingText, specLine)
		edit := doc.NodeEdit(packageClause, []byte(newText), "add Go import "+modulePath)
		return &edit
	}

	return nil
}

// RemoveImport removes an import for modulePath from a Go source file.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	root := doc.Tree.RootNode()
	var result *rewrite.Edit

	walk(root, func(n *tree_sitter.Node) {
		if result != nil || n.Kind() != "import_spec" {
			return
		}
		pathNode := n.ChildByFieldName("path")
		if pathNode == nil {
			return
		}
		importPath := strings.Trim(pathNode.Utf8Text(doc.Source), "\"`")
		if importPath != modulePath {
			return
		}

		parent := n.Parent()
		if parent != nil && parent.Kind() == "import_declaration" {
			// Single `import "modulePath"` declaration -> remove whole line including trailing newline
			startB, endB := expandToFullLine(doc.Source, parent.StartByte(), parent.EndByte())
			result = &rewrite.Edit{
				StartByte:   startB,
				EndByte:     endB,
				Replacement: nil,
				Description: "remove Go import " + modulePath,
			}
			return
		}

		// Inside an `import ( ... )` block -> remove the spec's full line
		startB, endB := expandToFullLine(doc.Source, n.StartByte(), n.EndByte())
		result = &rewrite.Edit{
			StartByte:   startB,
			EndByte:     endB,
			Replacement: nil,
			Description: "remove Go import " + modulePath,
		}
	})

	return result
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
