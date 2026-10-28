// Package java implements the Java LanguageProvider for code-graft.
package java

import (
	"fmt"
	"strings"

	"github.com/loganprice/code-graft/internal/lang"
	"github.com/loganprice/code-graft/pkg/rewrite"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
)

// Provider implements lang.LanguageProvider for Java.
type Provider struct {
	language *tree_sitter.Language
}

// NewProvider returns a new Java LanguageProvider.
func NewProvider() *Provider {
	return &Provider{
		language: tree_sitter.NewLanguage(tree_sitter_java.Language()),
	}
}

// Name returns "java".
func (p *Provider) Name() string {
	return "java"
}

// Extensions returns [".java"].
func (p *Provider) Extensions() []string {
	return []string{".java"}
}

// Language returns the Java Tree-sitter grammar.
func (p *Provider) Language() *tree_sitter.Language {
	return p.language
}

// ExpressionWrappers wraps Java expression or statement snippets inside a dummy class method
// so they parse cleanly into CST subtrees.
func (p *Provider) ExpressionWrappers() []rewrite.ExpressionWrapperFunc {
	return []rewrite.ExpressionWrapperFunc{
		// 1. Expression wrapper: `class _C { void _m() { Object _x = <snippet>; } }`
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			wrapped := fmt.Sprintf("class _C {\nvoid _m() {\nObject _x = %s;\n}\n}\n", snippet)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && n.Kind() == "variable_declarator" {
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
		// 2. Statement wrapper: `class _C { void _m() { <snippet>; } }`
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			semi := snippet
			if !strings.HasSuffix(strings.TrimSpace(semi), ";") && !strings.HasSuffix(strings.TrimSpace(semi), "}") {
				semi += ";"
			}
			wrapped := fmt.Sprintf("class _C {\nvoid _m() {\n%s\n}\n}\n", semi)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && n.Kind() == "method_declaration" {
						body := n.ChildByFieldName("body")
						if body != nil && body.NamedChildCount() > 0 {
							stmt := body.NamedChild(0)
							if stmt.Kind() == "expression_statement" && stmt.NamedChildCount() > 0 {
								found = stmt.NamedChild(0)
							} else {
								found = stmt
							}
						}
					}
				})
				return found
			}
			return wrapped, unwrap
		},
		// 3. Annotation / Class member wrapper: `<snippet> class _C {}` or `class _C { <snippet> void _m() {} }`
		func(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
			wrapped := fmt.Sprintf("class _C {\n%s\nvoid _m() {}\n}\n", snippet)
			unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
				var found *tree_sitter.Node
				walk(root, func(n *tree_sitter.Node) {
					if found == nil && (n.Kind() == "marker_annotation" || n.Kind() == "annotation") {
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

// AnalyzeScopeInfo extracts Java `import com.acme.sdk.AcmeClient;` declarations and tracks
// local variable declarations (`AcmeClient client = new AcmeClient(...)`) and field declarations.
func (p *Provider) AnalyzeScopeInfo(doc *rewrite.Document) *lang.ScopeInfo {
	scope := lang.NewScopeInfo()
	root := doc.Tree.RootNode()

	// Pass 1: Extract import_declaration nodes.
	walk(root, func(n *tree_sitter.Node) {
		if n.Kind() != "import_declaration" {
			return
		}
		text := strings.TrimSpace(n.Utf8Text(doc.Source))
		text = strings.TrimPrefix(text, "import")
		text = strings.TrimSpace(text)
		text = strings.TrimPrefix(text, "static")
		text = strings.TrimSpace(text)
		text = strings.TrimSuffix(text, ";")
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		// Map both the short class name (e.g. "AcmeClient") and the package prefix
		// so `fromPackage` can match either "com.acme.sdk" or "com.acme.sdk.AcmeClient".
		lastDot := strings.LastIndex(text, ".")
		if lastDot > 0 {
			shortName := text[lastDot+1:]
			pkgName := text[:lastDot]
			if shortName != "*" {
				scope.Imports[shortName] = pkgName
				scope.Imports[text] = pkgName
			}
		}
	})

	// Pass 2: Track local_variable_declaration and field_declaration.
	walk(root, func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "local_variable_declaration", "field_declaration":
			typeNode := n.ChildByFieldName("type")
			var typePkg string
			if typeNode != nil {
				typeName := strings.TrimSpace(typeNode.Utf8Text(doc.Source))
				if pkg, ok := scope.Imports[typeName]; ok {
					typePkg = pkg
				}
			}
			for i := uint(0); i < n.NamedChildCount(); i++ {
				decl := n.NamedChild(i)
				if decl.Kind() == "variable_declarator" {
					nameNode := decl.ChildByFieldName("name")
					valNode := decl.ChildByFieldName("value")
					if nameNode == nil {
						continue
					}
					varName := nameNode.Utf8Text(doc.Source)
					if typePkg != "" {
						scope.VarOrigins[varName] = typePkg
						continue
					}
					if valNode != nil {
						if pkg := inferJavaExprOrigin(valNode, doc.Source, scope); pkg != "" {
							scope.VarOrigins[varName] = pkg
						}
					}
				}
			}
		}
	})

	return scope
}

func inferJavaExprOrigin(expr *tree_sitter.Node, src []byte, scope *lang.ScopeInfo) string {
	if expr == nil {
		return ""
	}
	switch expr.Kind() {
	case "object_creation_expression":
		typeNode := expr.ChildByFieldName("type")
		if typeNode != nil {
			tName := strings.TrimSpace(typeNode.Utf8Text(src))
			if pkg, ok := scope.Imports[tName]; ok {
				return pkg
			}
		}
	case "method_invocation":
		obj := expr.ChildByFieldName("object")
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
	return ""
}

// AddImport inserts an `import <modulePath>.<symbolOrAlias>;` into a Java source file.
func (p *Provider) AddImport(doc *rewrite.Document, modulePath string, symbolOrAlias string) *rewrite.Edit {
	fullImport := modulePath
	if symbolOrAlias != "" && !strings.HasSuffix(modulePath, "."+symbolOrAlias) {
		fullImport = modulePath + "." + symbolOrAlias
	}

	scope := p.AnalyzeScopeInfo(doc)
	if _, exists := scope.Imports[fullImport]; exists {
		return nil
	}
	if symbolOrAlias != "" {
		if existingPkg, ok := scope.Imports[symbolOrAlias]; ok && existingPkg == modulePath {
			return nil
		}
	}

	importStmt := fmt.Sprintf("import %s;", fullImport)
	root := doc.Tree.RootNode()
	var lastImport *tree_sitter.Node
	var packageDecl *tree_sitter.Node

	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		switch c.Kind() {
		case "package_declaration":
			packageDecl = c
		case "import_declaration":
			lastImport = c
		}
	}

	if lastImport != nil {
		existing := lastImport.Utf8Text(doc.Source)
		edit := doc.NodeEdit(lastImport, []byte(existing+"\n"+importStmt), "add Java import "+fullImport)
		return &edit
	}
	if packageDecl != nil {
		existing := packageDecl.Utf8Text(doc.Source)
		edit := doc.NodeEdit(packageDecl, []byte(existing+"\n\n"+importStmt), "add Java import "+fullImport)
		return &edit
	}
	return &rewrite.Edit{
		StartByte:   0,
		EndByte:     0,
		Replacement: []byte(importStmt + "\n"),
		Description: "add Java import " + fullImport,
	}
}

// RemoveImport removes an import matching modulePath (either exact class import or package prefix) from a Java file.
func (p *Provider) RemoveImport(doc *rewrite.Document, modulePath string) *rewrite.Edit {
	root := doc.Tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() != "import_declaration" {
			continue
		}
		text := strings.TrimSpace(c.Utf8Text(doc.Source))
		if strings.Contains(text, modulePath+";") || strings.Contains(text, modulePath+".") {
			startB, endB := expandToFullLine(doc.Source, c.StartByte(), c.EndByte())
			return &rewrite.Edit{
				StartByte:   startB,
				EndByte:     endB,
				Replacement: nil,
				Description: "remove Java import " + modulePath,
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
