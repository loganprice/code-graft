// Package ruby implements the Ruby LanguageProvider for code-graft.
package ruby

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
)

// Provider implements lang.LanguageProvider for Ruby.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new Ruby LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_ruby.Language()),
	}
}

// Name returns "ruby".
func (p *Provider) Name() string {
	return "ruby"
}

// Extensions returns [".rb"].
func (p *Provider) Extensions() []string {
	return []string{".rb"}
}

// Language returns the Ruby Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers returns expression wrappers for Ruby snippets.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return nil
}

// AnalyzeScope implements rewrite.LanguageSupport.
func (p *Provider) AnalyzeScope(doc *rewrite.Document) rewrite.ScopeVerifier {
	return p.AnalyzeScopeInfo(doc)
}

var rubyRequireRegex = regexp.MustCompile(`^require(?:_relative)?\s*\(?\s*['"]([^'"]+)['"]\s*\)?$`)

// AnalyzeScopeInfo extracts Ruby `require 'acme_sdk'` calls and tracks local variable
// assignments (`client = AcmeSdk::Client.new(...)`).
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	scope := lang.NewScopeInfo()
	root := doc.Tree.RootNode()

	// Pass 1: Extract `require 'gem_name'` calls.
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "call" {
			return
		}
		text := strings.TrimSpace(n.Utf8Text(doc.Source))
		if sub := rubyRequireRegex.FindStringSubmatch(text); len(sub) == 2 {
			reqPath := sub[1]
			scope.Imports[reqPath] = reqPath
			// Also map CamelCase module heuristic (e.g. `acme_sdk` -> `AcmeSdk`)
			camel := snakeToCamel(reqPath)
			if camel != "" {
				scope.Imports[camel] = reqPath
			}
		}
	})

	// Pass 2: Track `assignment` (`client = AcmeSdk::Client.new(...)`)
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "assignment" {
			return
		}
		left := n.ChildByFieldName("left")
		right := n.ChildByFieldName("right")
		if left == nil || right == nil {
			return
		}
		varName := strings.TrimSpace(left.Utf8Text(doc.Source))
		rhsText := strings.TrimSpace(right.Utf8Text(doc.Source))
		for modKey, gemName := range scope.Imports {
			if strings.HasPrefix(rhsText, modKey+"::") || strings.HasPrefix(rhsText, modKey+".") {
				scope.VarOrigins[varName] = gemName
				break
			}
		}
	})

	return scope
}

func snakeToCamel(s string) string {
	// Strip path prefix if any
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '_' || r == '-'
	})
	var b strings.Builder
	for _, p := range parts {
		if len(p) > 0 {
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}

// AddImport inserts `require '<modulePath>'` into a Ruby source file.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, symbolOrAlias string) *rewrite.Edit {
	reqStmt := fmt.Sprintf("require '%s'", modulePath)
	if bytes.Contains(doc.Source, []byte(reqStmt)) || bytes.Contains(doc.Source, []byte(fmt.Sprintf(`require "%s"`, modulePath))) {
		return nil
	}

	root := doc.Tree.RootNode()
	var lastRequire *tree_sitter.Node
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() == "call" && rubyRequireRegex.MatchString(strings.TrimSpace(c.Utf8Text(doc.Source))) {
			lastRequire = c
		}
	}

	if lastRequire != nil {
		existing := lastRequire.Utf8Text(doc.Source)
		edit := doc.NodeEdit(lastRequire, []byte(existing+"\n"+reqStmt), "add Ruby require "+modulePath)
		return &edit
	}
	return &rewrite.Edit{
		StartByte:   0,
		EndByte:     0,
		Replacement: []byte(reqStmt + "\n"),
		Description: "add Ruby require " + modulePath,
	}
}

// RemoveImport removes `require '<modulePath>'` from a Ruby source file.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	root := doc.Tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() != "call" {
			continue
		}
		if sub := rubyRequireRegex.FindStringSubmatch(strings.TrimSpace(c.Utf8Text(doc.Source))); len(sub) == 2 && sub[1] == modulePath {
			startB, endB := expandToFullLine(doc.Source, c.StartByte(), c.EndByte())
			return &rewrite.Edit{
				StartByte:   startB,
				EndByte:     endB,
				Replacement: nil,
				Description: "remove Ruby require " + modulePath,
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
