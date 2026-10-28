// Package typescript implements the TypeScript/JavaScript LanguageProvider for code-graft.
package typescript

import (
	"fmt"
	"strings"

	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// Provider implements lang.LanguageProvider for TypeScript and JavaScript.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new TypeScript/JavaScript LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()),
	}
}

// Name returns "typescript".
func (p *Provider) Name() string {
	return "typescript"
}

// Extensions returns [".ts", ".tsx", ".js", ".jsx", ".mjs"].
func (p *Provider) Extensions() []string {
	return []string{".ts", ".tsx", ".js", ".jsx", ".mjs"}
}

// Language returns the TypeScript Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers returns expression wrappers for TypeScript snippets.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return nil
}

// AnalyzeScope implements rewrite.LanguageSupport.
func (p *Provider) AnalyzeScope(doc *rewrite.Document) rewrite.ScopeVerifier {
	return p.AnalyzeScopeInfo(doc)
}

// AnalyzeScopeInfo extracts ES module imports (`import { Client } from '@acme/sdk'`,
// `import * as sdk from '@acme/sdk'`, `import sdk from '@acme/sdk'`) and tracks local
// variable assignments (`const client = new Client()`, `const client = sdk.createClient()`).
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	scope := lang.NewScopeInfo()
	root := doc.Tree.RootNode()

	// Pass 1: Extract import_statement nodes.
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "import_statement" {
			return
		}
		sourceNode := n.ChildByFieldName("source")
		if sourceNode == nil {
			return
		}
		modulePath := strings.Trim(sourceNode.Utf8Text(doc.Source), "\"'`")
		if modulePath == "" {
			return
		}

		// Walk import_clause for named imports, namespace imports, and default imports.
		walk(n, func(c *tree_sitter.Node) {
			switch c.Kind() {
			case "import_specifier":
				// `import { Foo } from 'pkg'` or `import { Foo as Bar } from 'pkg'`
				aliasNode := c.ChildByFieldName("alias")
				nameNode := c.ChildByFieldName("name")
				if aliasNode != nil {
					scope.Imports[aliasNode.Utf8Text(doc.Source)] = modulePath
				} else if nameNode != nil {
					scope.Imports[nameNode.Utf8Text(doc.Source)] = modulePath
				}
			case "namespace_import":
				// `import * as sdk from 'pkg'`
				for i := uint(0); i < c.NamedChildCount(); i++ {
					id := c.NamedChild(i)
					if id.Kind() == "identifier" {
						scope.Imports[id.Utf8Text(doc.Source)] = modulePath
					}
				}
			case "import_clause":
				// Default import: `import sdk from 'pkg'` (direct identifier child of import_clause)
				for i := uint(0); i < c.NamedChildCount(); i++ {
					ch := c.NamedChild(i)
					if ch.Kind() == "identifier" {
						scope.Imports[ch.Utf8Text(doc.Source)] = modulePath
					}
				}
			}
		})
	})

	// Pass 2: Track variable_declarator (`const client = new Client(...)` or `sdk.create(...)`)
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "variable_declarator" {
			return
		}
		nameNode := n.ChildByFieldName("name")
		valNode := n.ChildByFieldName("value")
		if nameNode == nil || valNode == nil {
			return
		}
		varName := nameNode.Utf8Text(doc.Source)
		if pkg := inferTSExprOrigin(valNode, doc.Source, scope); pkg != "" {
			scope.VarOrigins[varName] = pkg
		}
	})

	return scope
}

func inferTSExprOrigin(expr *tree_sitter.Node, src []byte, scope *lang.ScopeInfo) string {
	if expr == nil {
		return ""
	}
	switch expr.Kind() {
	case "new_expression":
		ctor := expr.ChildByFieldName("constructor")
		if ctor != nil {
			if ctor.Kind() == "identifier" {
				if pkg, ok := scope.Imports[ctor.Utf8Text(src)]; ok {
					return pkg
				}
			} else if ctor.Kind() == "member_expression" {
				obj := ctor.ChildByFieldName("object")
				if obj != nil {
					if pkg, ok := scope.Imports[obj.Utf8Text(src)]; ok {
						return pkg
					}
				}
			}
		}
	case "call_expression":
		fn := expr.ChildByFieldName("function")
		if fn != nil {
			if fn.Kind() == "identifier" {
				if pkg, ok := scope.Imports[fn.Utf8Text(src)]; ok {
					return pkg
				}
			} else if fn.Kind() == "member_expression" {
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
	case "await_expression":
		if expr.NamedChildCount() > 0 {
			return inferTSExprOrigin(expr.NamedChild(0), src, scope)
		}
	}
	return ""
}

// AddImport inserts an ES module import for modulePath and symbolOrAlias into a TypeScript/JS file.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, symbolOrAlias string) *rewrite.Edit {
	scope := p.AnalyzeScopeInfo(doc)
	for sym, existingPath := range scope.Imports {
		if existingPath == modulePath && (symbolOrAlias == "" || sym == symbolOrAlias) {
			return nil
		}
	}

	var importStmt string
	if symbolOrAlias == "" {
		importStmt = fmt.Sprintf("import '%s';", modulePath)
	} else if strings.HasPrefix(symbolOrAlias, "* as ") {
		importStmt = fmt.Sprintf("import %s from '%s';", symbolOrAlias, modulePath)
	} else {
		importStmt = fmt.Sprintf("import { %s } from '%s';", symbolOrAlias, modulePath)
	}

	root := doc.Tree.RootNode()
	var lastImport *tree_sitter.Node
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() == "import_statement" {
			lastImport = c
		}
	}

	if lastImport != nil {
		existing := lastImport.Utf8Text(doc.Source)
		newText := fmt.Sprintf("%s\n%s", existing, importStmt)
		edit := doc.NodeEdit(lastImport, []byte(newText), "add TS import "+modulePath)
		return &edit
	}

	return &rewrite.Edit{
		StartByte:   0,
		EndByte:     0,
		Replacement: []byte(importStmt + "\n"),
		Description: "add TS import " + modulePath,
	}
}

// RemoveImport removes an import_statement matching modulePath from a TypeScript/JS file.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	root := doc.Tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() != "import_statement" {
			continue
		}
		sourceNode := c.ChildByFieldName("source")
		if sourceNode == nil {
			continue
		}
		mod := strings.Trim(sourceNode.Utf8Text(doc.Source), "\"'`")
		if mod == modulePath {
			startB, endB := expandToFullLine(doc.Source, c.StartByte(), c.EndByte())
			return &rewrite.Edit{
				StartByte:   startB,
				EndByte:     endB,
				Replacement: nil,
				Description: "remove TS import " + modulePath,
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
