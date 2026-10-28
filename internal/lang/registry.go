// Package lang provides language-specific Tree-sitter grammars, import managers,
// and Tier 1 intra-file semantic scope analyzers for Go, TypeScript, and Python.
package lang

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/loganprice/code-graft/pkg/rewrite"
)

// ScopeInfo holds intra-file semantic information extracted from a source file's CST:
//   - Imports maps a local identifier/alias (e.g., "sdk", "Client") to its canonical
//     package or module path (e.g., "github.com/acme/sdk", "@acme/sdk").
//   - VarOrigins maps a local variable name (e.g., "client") to the canonical package
//     or module path from which it was constructed or assigned (e.g., "github.com/acme/sdk").
type ScopeInfo struct {
	Imports    map[string]string
	VarOrigins map[string]string
}

// NewScopeInfo initializes an empty ScopeInfo.
func NewScopeInfo() *ScopeInfo {
	return &ScopeInfo{
		Imports:    make(map[string]string),
		VarOrigins: make(map[string]string),
	}
}

// HasPackage check returns true if any symbol in the file is imported from targetPackage.
func (s *ScopeInfo) HasPackage(targetPackage string) bool {
	if targetPackage == "" {
		return true
	}
	for _, pkg := range s.Imports {
		if pkg == targetPackage {
			return true
		}
	}
	return false
}

// SymbolBelongsToPackage checks whether a matched identifier (e.g., the `$CLIENT` capture
// in `$CLIENT.GetUser(...)` or the package qualifier in `sdk.NewClient()`) originates from
// targetPackage according to the file's imports and local variable assignments.
func (s *ScopeInfo) SymbolBelongsToPackage(identifier string, targetPackage string) bool {
	if targetPackage == "" {
		return true
	}
	id := strings.TrimSpace(identifier)
	if pkg, ok := s.Imports[id]; ok && pkg == targetPackage {
		return true
	}
	if pkg, ok := s.VarOrigins[id]; ok && pkg == targetPackage {
		return true
	}
	return false
}

// LookupSymbolPackage returns the canonical package or module path from which `identifier`
// originates (either as an imported package/symbol alias or as a tracked local variable),
// or "" if unknown.
func (s *ScopeInfo) LookupSymbolPackage(identifier string) string {
	id := strings.TrimSpace(identifier)
	if pkg, ok := s.VarOrigins[id]; ok {
		return pkg
	}
	if pkg, ok := s.Imports[id]; ok {
		return pkg
	}
	return ""
}

// ImportedPackages returns a copy of the map from local alias/symbol to imported package path.
func (s *ScopeInfo) ImportedPackages() map[string]string {
	out := make(map[string]string, len(s.Imports))
	for k, v := range s.Imports {
		out[k] = v
	}
	return out
}

// LanguageProvider encapsulates language-specific Tree-sitter grammar, snippet expression
// wrappers, intra-file scope analysis, and lossless import management.
type LanguageProvider interface {
	rewrite.LanguageSupport
	// AnalyzeScopeInfo extracts the concrete *ScopeInfo from doc.
	AnalyzeScopeInfo(doc *rewrite.Document) *ScopeInfo
}

// Registry manages registered LanguageProviders by name and file extension.
type Registry struct {
	byName map[string]LanguageProvider
	byExt  map[string]LanguageProvider
}

// NewRegistry creates a Registry pre-populated with the given providers.
func NewRegistry(providers ...LanguageProvider) *Registry {
	r := &Registry{
		byName: make(map[string]LanguageProvider),
		byExt:  make(map[string]LanguageProvider),
	}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

// Register adds a LanguageProvider to the registry.
func (r *Registry) Register(p LanguageProvider) {
	r.byName[strings.ToLower(p.Name())] = p
	for _, ext := range p.Extensions() {
		r.byExt[strings.ToLower(ext)] = p
	}
}

// ByName looks up a provider by language name (e.g., "go", "typescript", "ts", "python", "py").
func (r *Registry) ByName(name string) (LanguageProvider, error) {
	norm := strings.ToLower(strings.TrimSpace(name))
	switch norm {
	case "golang":
		norm = "go"
	case "ts", "js", "javascript", "tsx":
		norm = "typescript"
	case "py":
		norm = "python"
	case "rs":
		norm = "rust"
	case "rb":
		norm = "ruby"
	case "c++", "cxx", "cc", "hpp":
		norm = "cpp"
	}
	if p, ok := r.byName[norm]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unsupported language %q", name)
}

// ByPath looks up a provider by file extension.
func (r *Registry) ByPath(path string) (LanguageProvider, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	p, ok := r.byExt[ext]
	return p, ok
}
