// Package c implements the C LanguageProvider for code-graft.
package c

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_c "github.com/tree-sitter/tree-sitter-c/bindings/go"
)

// Provider implements lang.LanguageProvider for C.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new C LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_c.Language()),
	}
}

// Name returns "c".
func (p *Provider) Name() string {
	return "c"
}

// Extensions returns [".c", ".h"].
func (p *Provider) Extensions() []string {
	return []string{".c", ".h"}
}

// Language returns the C Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers wraps C expression snippets inside `void _f() { int _x = <snippet>; }`
// so they parse cleanly into CST subtrees.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return []rewrite.ExpressionWrapperFunc{
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			wrapped := fmt.Sprintf("void _f() {\nint _x = %s;\n}\n", snippet)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && n.Kind() == "init_declarator" {
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

// AnalyzeScopeInfo extracts C `#include` headers (`preproc_include`) into ScopeInfo.
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	scope := lang.NewScopeInfo()
	root := doc.Tree.RootNode()

	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "preproc_include" {
			return
		}
		pathNode := n.ChildByFieldName("path")
		if pathNode == nil {
			return
		}
		header := strings.Trim(strings.TrimSpace(pathNode.Utf8Text(doc.Source)), `"<>`)
		if header != "" {
			scope.Imports[header] = header
		}
	})

	return scope
}

// AddImport inserts `#include <header>` or `#include "header"` into a C file.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, symbolOrAlias string) *rewrite.Edit {
	incStmt := formatInclude(modulePath)
	if bytes.Contains(doc.Source, []byte(incStmt)) {
		return nil
	}

	root := doc.Tree.RootNode()
	var lastInclude *tree_sitter.Node
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() == "preproc_include" {
			lastInclude = c
		}
	}

	if lastInclude != nil {
		existing := strings.TrimSuffix(lastInclude.Utf8Text(doc.Source), "\n")
		edit := doc.NodeEdit(lastInclude, []byte(existing+"\n"+incStmt+"\n"), "add C include "+modulePath)
		return &edit
	}
	return &rewrite.Edit{
		StartByte:   0,
		EndByte:     0,
		Replacement: []byte(incStmt + "\n"),
		Description: "add C include " + modulePath,
	}
}

// RemoveImport removes a `#include` matching modulePath from a C file.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	root := doc.Tree.RootNode()
	cleanTarget := strings.Trim(modulePath, `"<>`)
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() != "preproc_include" {
			continue
		}
		pathNode := c.ChildByFieldName("path")
		if pathNode == nil {
			continue
		}
		header := strings.Trim(strings.TrimSpace(pathNode.Utf8Text(doc.Source)), `"<>`)
		if header == cleanTarget {
			startB, endB := expandToFullLine(doc.Source, c.StartByte(), c.EndByte())
			return &rewrite.Edit{
				StartByte:   startB,
				EndByte:     endB,
				Replacement: nil,
				Description: "remove C include " + modulePath,
			}
		}
	}
	return nil
}

func formatInclude(header string) string {
	trimmed := strings.TrimSpace(header)
	if strings.HasPrefix(trimmed, "<") || strings.HasPrefix(trimmed, `"`) {
		return "#include " + trimmed
	}
	return fmt.Sprintf(`#include "%s"`, trimmed)
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
