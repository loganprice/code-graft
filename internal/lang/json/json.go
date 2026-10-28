// Package json implements the JSON LanguageProvider for code-graft.
package json

import (
	"fmt"
	"regexp"

	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_json "github.com/tree-sitter/tree-sitter-json/bindings/go"
)

var unquotedPlaceholderRegex = regexp.MustCompile(`(^|[:\[,\s])(__ts_(?:meta|variadic)_[A-Za-z0-9_]+__)([,\]\}\s]|$)`)

// Provider implements lang.LanguageProvider for JSON files.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new JSON LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_json.Language()),
	}
}

// Name returns "json".
func (p *Provider) Name() string {
	return "json"
}

// Extensions returns [".json"].
func (p *Provider) Extensions() []string {
	return []string{".json"}
}

// Language returns the JSON Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers wraps JSON pair snippets (`"key": $VAL`) inside a dummy object `{ ... }`
// so key-value pairs can be matched directly without writing outer braces.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return []rewrite.ExpressionWrapperFunc{
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			quoted := unquotedPlaceholderRegex.ReplaceAllString(snippet, `$1"$2"$3`)
			wrapped := fmt.Sprintf("{\n%s\n}\n", quoted)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && n.Kind() == "pair" {
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

// AnalyzeScopeInfo returns an empty ScopeInfo for JSON documents.
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	return lang.NewScopeInfo()
}

// AddImport is a no-op for JSON.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, symbolOrAlias string) *rewrite.Edit {
	return nil
}

// RemoveImport is a no-op for JSON.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	return nil
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
