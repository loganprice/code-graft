package rewrite

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// RecipeSpec defines a declarative OpenRewrite-style recipe loaded from YAML or constructed in Go.
// It supports enterprise metadata, parameterized template options (`Parameters`), preconditions,
// recursive recipe composition (`Includes` with `with` parameter bindings), code rewrite rules,
// manifest upgrades, and embedded self-test suites.
type RecipeSpec struct {
	Name          string                 `yaml:"name" json:"name"`
	Version       string                 `yaml:"version,omitempty" json:"version,omitempty"`
	Category      string                 `yaml:"category,omitempty" json:"category,omitempty"`
	Author        string                 `yaml:"author,omitempty" json:"author,omitempty"`
	Tags          []string               `yaml:"tags,omitempty" json:"tags,omitempty"`
	Description   string                 `yaml:"description" json:"description"`
	Parameters    map[string]ParamSpec   `yaml:"parameters,omitempty" json:"parameters,omitempty"`
	Preconditions *Preconditions         `yaml:"preconditions,omitempty" json:"preconditions,omitempty"`
	Verify        *VerifySpec            `yaml:"verify,omitempty" json:"verify,omitempty"`
	Includes      []IncludeSpec          `yaml:"includes,omitempty" json:"includes,omitempty"`
	Rules         []RewriteRule          `yaml:"rules,omitempty" json:"rules,omitempty"`
	Manifests     []ManifestRule         `yaml:"manifests,omitempty" json:"manifests,omitempty"`
	Tests         []RecipeTestSuite      `yaml:"tests,omitempty" json:"tests,omitempty"`
}

// ParamSpec declares a template parameter accepted by a parameterized RecipeSpec.
type ParamSpec struct {
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Default     string `yaml:"default,omitempty" json:"default,omitempty"`
}

// IncludeSpec supports both plain string paths (`- "./child.yaml"`) and parameterized
// template invocations (`- path: "./template.yaml"\n    with:\n      OLD_PKG: "foo"`).
type IncludeSpec struct {
	Path string            `yaml:"path" json:"path"`
	With map[string]string `yaml:"with,omitempty" json:"with,omitempty"`
}

// UnmarshalYAML allows IncludeSpec to be unmarshaled from either a YAML scalar string
// or a YAML mapping `{ path: ..., with: { ... } }`.
func (inc *IncludeSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		inc.Path = strings.TrimSpace(value.Value)
		return nil
	}
	type rawInclude IncludeSpec
	var raw rawInclude
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*inc = IncludeSpec(raw)
	return nil
}

// VerifySpec configures post-rewrite build & test verification commands and failure recovery behavior.
type VerifySpec struct {
	Commands          []string `yaml:"commands,omitempty" json:"commands,omitempty"`                   // shell commands executed in rootDir after applying edits (e.g. ["go test ./...", "mvn test-compile"])
	TimeoutSeconds    int      `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`       // per-command timeout in seconds (defaults to 300s)
	RollbackOnFailure *bool    `yaml:"rollbackOnFailure,omitempty" json:"rollbackOnFailure,omitempty"` // defaults to true: automatically restores modified files if any command fails
}

// ShouldRollback returns true if rollbackOnFailure is nil (default) or explicitly true.
func (v *VerifySpec) ShouldRollback() bool {
	if v == nil || v.RollbackOnFailure == nil {
		return true
	}
	return *v.RollbackOnFailure
}

// Preconditions defines repository-level and file-level guardrails that must hold
// before a recipe (or individual rule) is executed.
type Preconditions struct {
	RequiredFiles []string `yaml:"requiredFiles,omitempty" json:"requiredFiles,omitempty"` // repo-level: relative file paths or globs that must exist in rootDir
	FilePatterns  []string `yaml:"filePatterns,omitempty" json:"filePatterns,omitempty"`   // file-level: glob patterns that target path must match (at least one)
	ExcludeFiles  []string `yaml:"excludeFiles,omitempty" json:"excludeFiles,omitempty"`   // file-level: glob patterns that target path must NOT match
}

// RecipeTestSuite defines an embedded governance test case verifying that a recipe
// transforms Input into Expected (or leaves Input unchanged when Expected is empty or equal).
type RecipeTestSuite struct {
	Name     string `yaml:"name" json:"name"`
	Path     string `yaml:"path" json:"path"` // virtual file path, e.g. "OrderController.java" or "pom.xml"
	Input    string `yaml:"input" json:"input"`
	Expected string `yaml:"expected" json:"expected"`
}

// RewriteRule defines a single language-aware syntactic + Tier 1 semantic transformation step.
// A rule may either rewrite code (via `Replace`), emit rows to an enterprise DataTable (via `DataTable`), or both.
type RewriteRule struct {
	ID            string                      `yaml:"id,omitempty" json:"id,omitempty"`
	Language      string                      `yaml:"language" json:"language"` // "go", "typescript", "python", "java", etc.
	Match         string                      `yaml:"match,omitempty" json:"match,omitempty"`
	Replace       string                      `yaml:"replace,omitempty" json:"replace,omitempty"`             // optional when rule is a search-only DataTable emitter
	DataTable     *DataTableSpec              `yaml:"dataTable,omitempty" json:"dataTable,omitempty"`         // emits a structured row to a named DataTable on every match
	TargetVar     string                      `yaml:"targetVar,omitempty" json:"targetVar,omitempty"`         // e.g. "CLIENT" in "$CLIENT.Get(...)"
	FromPackage   string                      `yaml:"fromPackage,omitempty" json:"fromPackage,omitempty"`     // e.g. "github.com/acme/legacy-sdk"
	AddImports    []ImportSpec                `yaml:"addImports,omitempty" json:"addImports,omitempty"`       // applied when rule matches >= 1 time in file
	RemoveImports []string                    `yaml:"removeImports,omitempty" json:"removeImports,omitempty"` // applied when rule matches >= 1 time in file
	Transforms    map[string]MetavarTransform `yaml:"transforms,omitempty" json:"transforms,omitempty"`       // regex/case transforms on captured metavariables before rendering Replace
	Preconditions *Preconditions              `yaml:"preconditions,omitempty" json:"preconditions,omitempty"` // optional per-rule file preconditions
	Kind          string                      `yaml:"kind,omitempty" json:"kind,omitempty"`                   // optional CST node kind filter
	Regex         string                      `yaml:"regex,omitempty" json:"regex,omitempty"`                 // optional regex on matched node text
	Where         map[string]string           `yaml:"where,omitempty" json:"where,omitempty"`                 // regex constraints on captured metavariables
	Inside        *RuleConstraint             `yaml:"inside,omitempty" json:"inside,omitempty"`               // relational: must be inside an ancestor matching constraint
	Has           *RuleConstraint             `yaml:"has,omitempty" json:"has,omitempty"`                     // relational: must contain a descendant matching constraint
	Follows       *RuleConstraint             `yaml:"follows,omitempty" json:"follows,omitempty"`             // relational: must follow a preceding sibling matching constraint
	Precedes      *RuleConstraint             `yaml:"precedes,omitempty" json:"precedes,omitempty"`           // relational: must precede a subsequent sibling matching constraint
	All           []RuleConstraint            `yaml:"all,omitempty" json:"all,omitempty"`                     // logical AND of constraints
	Any           []RuleConstraint            `yaml:"any,omitempty" json:"any,omitempty"`                     // logical OR of constraints
	Not           *RuleConstraint             `yaml:"not,omitempty" json:"not,omitempty"`                     // logical NOT of constraint
}

// DataTableSpec configures structured row emission for Codebase Intelligence & Impact Analysis.
// Column values can reference `$META` captures, `$0` (matched snippet), `$FILE`, `$LINE`, `$COL`, `$RULE_ID`, or `$PACKAGE`.
type DataTableSpec struct {
	Name    string            `yaml:"name" json:"name"`
	Columns map[string]string `yaml:"columns,omitempty" json:"columns,omitempty"`
}

// MetavarTransform defines a regex replacement or case conversion applied to a captured metavariable.
type MetavarTransform struct {
	Regex   string `yaml:"regex,omitempty" json:"regex,omitempty"`
	Replace string `yaml:"replace,omitempty" json:"replace,omitempty"`
}

// RuleConstraint represents a composable structural, relational, or logical AST predicate.
// It can be nested arbitrarily (e.g. `not: { inside: { kind: "if_statement", has: { pattern: "return $ERR" } } }`).
type RuleConstraint struct {
	Pattern  string            `yaml:"pattern,omitempty" json:"pattern,omitempty"`   // code-snippet pattern (supports $META unification)
	Kind     string            `yaml:"kind,omitempty" json:"kind,omitempty"`         // Tree-sitter node kind (e.g. "function_declaration", "if_statement")
	Regex    string            `yaml:"regex,omitempty" json:"regex,omitempty"`       // regex matched against candidate node's source text
	StopBy   string            `yaml:"stopBy,omitempty" json:"stopBy,omitempty"`     // "neighbor" (immediate parent/child/sibling) or "end" (default: full ancestor/descendant/sibling chain)
	Where    map[string]string `yaml:"where,omitempty" json:"where,omitempty"`       // regex constraints on captured metavariables
	Inside   *RuleConstraint   `yaml:"inside,omitempty" json:"inside,omitempty"`     // ancestor check
	Has      *RuleConstraint   `yaml:"has,omitempty" json:"has,omitempty"`           // descendant check
	Follows  *RuleConstraint   `yaml:"follows,omitempty" json:"follows,omitempty"`   // preceding sibling check
	Precedes *RuleConstraint   `yaml:"precedes,omitempty" json:"precedes,omitempty"` // succeeding sibling check
	All      []RuleConstraint  `yaml:"all,omitempty" json:"all,omitempty"`           // logical AND
	Any      []RuleConstraint  `yaml:"any,omitempty" json:"any,omitempty"`           // logical OR
	Not      *RuleConstraint   `yaml:"not,omitempty" json:"not,omitempty"`           // logical NOT
}

// RootConstraint extracts the top-level composite RuleConstraint from a RewriteRule.
func (r *RewriteRule) RootConstraint() RuleConstraint {
	return RuleConstraint{
		Pattern:  r.Match,
		Kind:     r.Kind,
		Regex:    r.Regex,
		Where:    r.Where,
		Inside:   r.Inside,
		Has:      r.Has,
		Follows:  r.Follows,
		Precedes: r.Precedes,
		All:      r.All,
		Any:      r.Any,
		Not:      r.Not,
	}
}

// ImportSpec specifies a module/package path and optional symbol or alias to import.
type ImportSpec struct {
	Module string `yaml:"module" json:"module"`
	Symbol string `yaml:"symbol,omitempty" json:"symbol,omitempty"`
}

// ManifestRule defines a dependency upgrade for go.mod, package.json, or requirements.txt / pyproject.toml.
type ManifestRule struct {
	Ecosystem  string `yaml:"ecosystem" json:"ecosystem"`
	OldPackage string `yaml:"oldPackage" json:"oldPackage"`
	NewPackage string `yaml:"newPackage,omitempty" json:"newPackage,omitempty"`
	NewVersion string `yaml:"newVersion" json:"newVersion"`
}

// LoadRecipeYAML reads and parses a YAML RecipeSpec from disk, recursively resolving
// any `includes` (relative to the recipe file's directory) with cycle detection.
func LoadRecipeYAML(path string) (*RecipeSpec, error) {
	return LoadRecipeYAMLWithParams(path, nil)
}

// LoadRecipeYAMLWithParams loads a YAML RecipeSpec from disk and binds the provided
// template parameters (`{{PARAM}}` or `${{PARAM}}`), validating required parameters
// and applying defaults before expanding `includes`.
func LoadRecipeYAMLWithParams(path string, params map[string]string) (*RecipeSpec, error) {
	visited := make(map[string]bool)
	return loadRecipeYAMLRecursive(path, params, visited)
}

func loadRecipeYAMLRecursive(path string, providedParams map[string]string, visited map[string]bool) (*RecipeSpec, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving path %s: %w", path, err)
	}
	visitKey := absPath + "|" + formatParamsKey(providedParams)
	if visited[visitKey] {
		return nil, fmt.Errorf("cyclic recipe include detected involving %s", absPath)
	}
	visited[visitKey] = true

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("reading recipe file %s: %w", path, err)
	}
	spec, err := ParseRecipeYAMLWithParams(data, providedParams)
	if err != nil {
		return nil, fmt.Errorf("in %s: %w", path, err)
	}

	if len(spec.Includes) == 0 {
		return spec, nil
	}

	baseDir := filepath.Dir(absPath)
	var composedRules []RewriteRule
	var composedManifests []ManifestRule
	var composedTests []RecipeTestSuite
	var composedVerifyCommands []string

	for idx, inc := range spec.Includes {
		incPath := inc.Path
		if !filepath.IsAbs(incPath) {
			incPath = filepath.Join(baseDir, incPath)
		}
		childVisited := make(map[string]bool, len(visited))
		for k, v := range visited {
			childVisited[k] = v
		}
		childSpec, err := loadRecipeYAMLRecursive(incPath, inc.With, childVisited)
		if err != nil {
			return nil, fmt.Errorf("including %q from %q: %w", inc.Path, spec.Name, err)
		}

		// Disambiguate rule IDs when the same parameterized template is included multiple times
		// with different `with` bindings.
		suffix := ""
		if len(inc.With) > 0 {
			suffix = fmt.Sprintf("#%d", idx+1)
		}

		for _, r := range childSpec.Rules {
			if suffix != "" && r.ID != "" {
				r.ID = r.ID + suffix
			}
			r.Preconditions = mergePreconditions(childSpec.Preconditions, r.Preconditions)
			composedRules = append(composedRules, r)
		}
		composedManifests = append(composedManifests, childSpec.Manifests...)
		composedTests = append(composedTests, childSpec.Tests...)
		if childSpec.Verify != nil {
			composedVerifyCommands = append(composedVerifyCommands, childSpec.Verify.Commands...)
		}
	}

	// Parent's own rules/manifests/tests run after included child recipes.
	composedRules = append(composedRules, spec.Rules...)
	composedManifests = append(composedManifests, spec.Manifests...)
	composedTests = append(composedTests, spec.Tests...)
	if len(composedVerifyCommands) > 0 {
		if spec.Verify == nil {
			spec.Verify = &VerifySpec{}
		}
		spec.Verify.Commands = append(composedVerifyCommands, spec.Verify.Commands...)
	}

	spec.Rules = composedRules
	spec.Manifests = composedManifests
	spec.Tests = composedTests
	return spec, nil
}

// ParseRecipeYAML parses YAML bytes into a RecipeSpec using default parameter values.
func ParseRecipeYAML(data []byte) (*RecipeSpec, error) {
	return ParseRecipeYAMLWithParams(data, nil)
}

var templatePlaceholderRegex = regexp.MustCompile(`\$?\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// ParseRecipeYAMLWithParams parses a YAML RecipeSpec, validates and resolves `parameters`
// against `providedParams` (and defaults), substitutes `{{PARAM}}` / `${{PARAM}}` placeholders,
// and returns the instantiated RecipeSpec.
func ParseRecipeYAMLWithParams(data []byte, providedParams map[string]string) (*RecipeSpec, error) {
	// Pass 1: Unmarshal header (`name` and `parameters`) to resolve defaults and check required params.
	var header struct {
		Name       string               `yaml:"name"`
		Parameters map[string]ParamSpec `yaml:"parameters"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("unmarshaling recipe YAML header: %w", err)
	}

	resolved := make(map[string]string, len(header.Parameters)+len(providedParams))
	for k, pSpec := range header.Parameters {
		if pSpec.Default != "" {
			resolved[k] = pSpec.Default
		}
	}
	for k, v := range providedParams {
		resolved[k] = v
	}

	// Verify all required parameters have a non-empty value.
	for k, pSpec := range header.Parameters {
		if pSpec.Required && strings.TrimSpace(resolved[k]) == "" {
			return nil, fmt.Errorf("missing required recipe parameter %q ( in recipe %q )", k, header.Name)
		}
	}

	// Substitute `{{PARAM}}` / `${{PARAM}}` across the YAML document if parameters are present.
	expandedData := data
	if len(resolved) > 0 {
		expandedStr := templatePlaceholderRegex.ReplaceAllStringFunc(string(data), func(m string) string {
			sub := templatePlaceholderRegex.FindStringSubmatch(m)
			if len(sub) == 2 {
				if val, ok := resolved[sub[1]]; ok {
					return val
				}
			}
			return m
		})
		expandedData = []byte(expandedStr)
	}

	var spec RecipeSpec
	if err := yaml.Unmarshal(expandedData, &spec); err != nil {
		return nil, fmt.Errorf("unmarshaling recipe YAML: %w", err)
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, fmt.Errorf("recipe must have a non-empty 'name'")
	}
	return &spec, nil
}

func formatParamsKey(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s;", k, params[k])
	}
	return b.String()
}

// MatchesFile checks whether a relative file path satisfies the file-level preconditions.
func (p *Preconditions) MatchesFile(relPath string) bool {
	if p == nil {
		return true
	}
	normPath := filepath.ToSlash(relPath)
	baseName := filepath.Base(normPath)

	// 1. Check ExcludeFiles first
	for _, pattern := range p.ExcludeFiles {
		if matchGlob(pattern, normPath, baseName) {
			return false
		}
	}

	// 2. Check FilePatterns (if non-empty, at least one must match)
	if len(p.FilePatterns) > 0 {
		matched := false
		for _, pattern := range p.FilePatterns {
			if matchGlob(pattern, normPath, baseName) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// MatchesRepo checks whether rootDir satisfies all repo-level `requiredFiles` preconditions.
func (p *Preconditions) MatchesRepo(rootDir string) bool {
	if p == nil || len(p.RequiredFiles) == 0 {
		return true
	}
	for _, req := range p.RequiredFiles {
		matches, err := filepath.Glob(filepath.Join(rootDir, req))
		if err != nil || len(matches) == 0 {
			return false
		}
	}
	return true
}

func matchGlob(pattern, slashPath, baseName string) bool {
	normPat := filepath.ToSlash(strings.TrimSpace(pattern))
	// Handle `**/foo` or `**/dir/**`
	if strings.Contains(normPat, "**") {
		parts := strings.Split(normPat, "**")
		allFound := true
		rem := slashPath
		for _, part := range parts {
			cleanPart := strings.Trim(part, "/")
			if cleanPart == "" {
				continue
			}
			// Check if cleanPart has wildcards like `*_test.go`
			if strings.ContainsAny(cleanPart, "*?") {
				if ok, _ := filepath.Match(cleanPart, baseName); !ok {
					allFound = false
					break
				}
			} else if idx := strings.Index(rem, cleanPart); idx >= 0 {
				rem = rem[idx+len(cleanPart):]
			} else {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}
	if ok, _ := filepath.Match(normPat, slashPath); ok {
		return true
	}
	if ok, _ := filepath.Match(normPat, baseName); ok {
		return true
	}
	return false
}

func mergePreconditions(parent, child *Preconditions) *Preconditions {
	if parent == nil {
		return child
	}
	if child == nil {
		return parent
	}
	return &Preconditions{
		RequiredFiles: append(append([]string{}, parent.RequiredFiles...), child.RequiredFiles...),
		FilePatterns:  append(append([]string{}, parent.FilePatterns...), child.FilePatterns...),
		ExcludeFiles:  append(append([]string{}, parent.ExcludeFiles...), child.ExcludeFiles...),
	}
}
