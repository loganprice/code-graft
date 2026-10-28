// Package rust implements the Rust LanguageProvider for code-graft.
package rust

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

// Provider implements lang.LanguageProvider for Rust.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new Rust LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_rust.Language()),
	}
}

// Name returns "rust".
func (p *Provider) Name() string {
	return "rust"
}

// Extensions returns [".rs"].
func (p *Provider) Extensions() []string {
	return []string{".rs"}
}

// Language returns the Rust Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers wraps Rust expression or statement snippets inside a dummy `fn _f()`
// so they parse cleanly into CST subtrees.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return []rewrite.ExpressionWrapperFunc{
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			wrapped := fmt.Sprintf("fn _f() {\nlet _ = %s;\n}\n", snippet)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && n.Kind() == "let_declaration" {
						val := n.ChildByFieldName("value")
						if val != nil {
							found = val
						}
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

// AnalyzeScopeInfo extracts Rust `use acme_sdk::Client;` declarations and tracks
// local `let client = Client::new(...)` or `let client = acme_sdk::Client::new(...)` bindings.
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	scope := lang.NewScopeInfo()
	root := doc.Tree.RootNode()

	// Pass 1: Extract use_declaration nodes (`use acme_sdk::Client;`, `use acme_sdk as sdk;`).
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "use_declaration" {
			return
		}
		text := strings.TrimSpace(n.Utf8Text(doc.Source))
		text = strings.TrimPrefix(text, "pub")
		text = strings.TrimSpace(text)
		text = strings.TrimPrefix(text, "use")
		text = strings.TrimSuffix(strings.TrimSpace(text), ";")
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}

		// Check for `use foo::bar as baz`
		if parts := strings.Split(text, " as "); len(parts) == 2 {
			pathPart := strings.TrimSpace(parts[0])
			alias := strings.TrimSpace(parts[1])
			crateRoot := strings.Split(pathPart, "::")[0]
			scope.Imports[alias] = crateRoot
			return
		}

		segments := strings.Split(text, "::")
		crateRoot := segments[0]
		lastSeg := segments[len(segments)-1]
		scope.Imports[crateRoot] = crateRoot
		if lastSeg != "*" && !strings.Contains(lastSeg, "{") {
			scope.Imports[lastSeg] = crateRoot
		}
	})

	// Pass 2: Track `let_declaration` (`let client = Client::new(...)` or `let mut c = ...`).
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "let_declaration" {
			return
		}
		patNode := n.ChildByFieldName("pattern")
		valNode := n.ChildByFieldName("value")
		if patNode == nil || valNode == nil {
			return
		}
		varName := strings.TrimPrefix(strings.TrimSpace(patNode.Utf8Text(doc.Source)), "mut ")
		varName = strings.TrimSpace(varName)
		if pkg := inferRustExprOrigin(valNode, doc.Source, scope); pkg != "" {
			scope.VarOrigins[varName] = pkg
		}
	})

	return scope
}

func inferRustExprOrigin(expr *tree_sitter.Node, src []byte, scope *lang.ScopeInfo) string {
	if expr == nil {
		return ""
	}
	switch expr.Kind() {
	case "call_expression":
		fn := expr.ChildByFieldName("function")
		if fn != nil {
			fnText := strings.TrimSpace(fn.Utf8Text(src))
			// e.g. `Client::new` or `acme_sdk::Client::new`
			if idx := strings.Index(fnText, "::"); idx > 0 {
				prefix := fnText[:idx]
				if pkg, ok := scope.Imports[prefix]; ok {
					return pkg
				}
			}
			if pkg, ok := scope.Imports[fnText]; ok {
				return pkg
			}
		}
	case "try_expression", "await_expression":
		if expr.NamedChildCount() > 0 {
			return inferRustExprOrigin(expr.NamedChild(0), src, scope)
		}
	}
	return ""
}

// AddImport inserts `use <modulePath>::<symbolOrAlias>;` (or `use <modulePath>;`) into a Rust file.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, symbolOrAlias string) *rewrite.Edit {
	usePath := modulePath
	if symbolOrAlias != "" && !strings.HasSuffix(modulePath, "::"+symbolOrAlias) {
		usePath = modulePath + "::" + symbolOrAlias
	}
	useStmt := fmt.Sprintf("use %s;", usePath)
	if bytes.Contains(doc.Source, []byte(useStmt)) {
		return nil
	}

	root := doc.Tree.RootNode()
	var lastUse *tree_sitter.Node
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() == "use_declaration" {
			lastUse = c
		}
	}

	if lastUse != nil {
		existing := lastUse.Utf8Text(doc.Source)
		edit := doc.NodeEdit(lastUse, []byte(existing+"\n"+useStmt), "add Rust use "+usePath)
		return &edit
	}
	return &rewrite.Edit{
		StartByte:   0,
		EndByte:     0,
		Replacement: []byte(useStmt + "\n"),
		Description: "add Rust use " + usePath,
	}
}

// RemoveImport removes a `use` declaration matching modulePath from a Rust file.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	root := doc.Tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() != "use_declaration" {
			continue
		}
		text := strings.TrimSpace(c.Utf8Text(doc.Source))
		if strings.Contains(text, "use "+modulePath+";") || strings.Contains(text, "use "+modulePath+"::") {
			startB, endB := expandToFullLine(doc.Source, c.StartByte(), c.EndByte())
			return &rewrite.Edit{
				StartByte:   startB,
				EndByte:     endB,
				Replacement: nil,
				Description: "remove Rust use " + modulePath,
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
