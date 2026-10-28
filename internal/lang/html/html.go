// Package html implements the HTML/XML LanguageProvider for code-graft.
package html

import (
	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_html "github.com/tree-sitter/tree-sitter-html/bindings/go"
)

// Provider implements lang.LanguageProvider for HTML and XML-like templates.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new HTML/XML LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_html.Language()),
	}
}

// Name returns "html".
func (p *Provider) Name() string {
	return "html"
}

// Extensions returns [".html", ".htm", ".xml", ".svg"].
func (p *Provider) Extensions() []string {
	return []string{".html", ".htm", ".xml", ".svg"}
}

// Language returns the HTML Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers returns expression wrappers for HTML snippets.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return nil
}

// AnalyzeScope implements rewrite.LanguageSupport.
func (p *Provider) AnalyzeScope(doc *rewrite.Document) rewrite.ScopeVerifier {
	return p.AnalyzeScopeInfo(doc)
}

// AnalyzeScopeInfo returns an empty ScopeInfo for HTML/XML documents.
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	return lang.NewScopeInfo()
}

// AddImport is a no-op for HTML/XML.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, symbolOrAlias string) *rewrite.Edit {
	return nil
}

// RemoveImport is a no-op for HTML/XML.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	return nil
}
