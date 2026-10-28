package rewrite

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// ScopeVerifier checks whether a captured symbol belongs to a target package/module.
type ScopeVerifier interface {
	SymbolBelongsToPackage(identifier string, targetPackage string) bool
	HasPackage(targetPackage string) bool
	LookupSymbolPackage(identifier string) string
	ImportedPackages() map[string]string
}

// LanguageSupport defines the language-specific hooks required by Engine.
type LanguageSupport interface {
	Name() string
	Extensions() []string
	Language() *tree_sitter.Language
	ExpressionWrappers() []ExpressionWrapperFunc
	AnalyzeScope(doc *Document) ScopeVerifier
	AddImport(doc *Document, modulePath string, symbolOrAlias string) *Edit
	RemoveImport(doc *Document, modulePath string) *Edit
}

// ManifestUpgraderFunc applies a ManifestRule to a manifest file's bytes.
type ManifestUpgraderFunc func(filePath string, content []byte, rule ManifestRule) ([]byte, bool)

// ConfigRewriterFunc applies structured configuration/infrastructure rules (YAML, .properties, Dockerfile)
// to a file buffer.
type ConfigRewriterFunc func(filePath string, content []byte, rules []RewriteRule) ([]byte, int, error)

// FileResult records the outcome of running a RecipeSpec against a single file.
type FileResult struct {
	Path         string         `json:"path"`
	Language     string         `json:"language"`
	Changed      bool           `json:"changed"`
	EditsApplied int            `json:"editsApplied"`
	Diff         string         `json:"diff,omitempty"`
	Rows         []DataTableRow `json:"rows,omitempty"`
	NewSource    []byte         `json:"-"`
}

// RunReport aggregates the results of a recipe execution across a workspace.
type RunReport struct {
	RecipeName   string                    `json:"recipeName"`
	FilesScanned int                       `json:"filesScanned"`
	FilesChanged int                       `json:"filesChanged"`
	Impact       ImpactSummary             `json:"impact"`
	Verification *VerificationResult       `json:"verification,omitempty"`
	DataTables   map[string][]DataTableRow `json:"dataTables,omitempty"`
	Results      []FileResult              `json:"results"`
}

// Engine coordinates polyglot recipe execution across source files and manifests.
type Engine struct {
	byName           map[string]LanguageSupport
	byExt            map[string]LanguageSupport
	manifestUpgrader ManifestUpgraderFunc
	configRewriter   ConfigRewriterFunc
}

// NewEngine creates a new polyglot rewriting Engine.
func NewEngine(manifestUpgrader ManifestUpgraderFunc, providers ...LanguageSupport) *Engine {
	e := &Engine{
		byName:           make(map[string]LanguageSupport),
		byExt:            make(map[string]LanguageSupport),
		manifestUpgrader: manifestUpgrader,
	}
	for _, p := range providers {
		e.RegisterLanguage(p)
	}
	return e
}

// SetConfigRewriter registers a structured configuration/infrastructure rewriter (for YAML, .properties, Dockerfile).
func (e *Engine) SetConfigRewriter(fn ConfigRewriterFunc) {
	e.configRewriter = fn
}

// RegisterLanguage registers a language provider with the Engine.
func (e *Engine) RegisterLanguage(p LanguageSupport) {
	e.byName[strings.ToLower(p.Name())] = p
	for _, ext := range p.Extensions() {
		e.byExt[strings.ToLower(ext)] = p
	}
}

// RunOnBytes executes a RecipeSpec on an in-memory file buffer identified by path.
func (e *Engine) RunOnBytes(path string, source []byte, recipe *RecipeSpec) (FileResult, error) {
	// 0. Check top-level file preconditions (`filePatterns` / `excludeFiles`).
	if !recipe.Preconditions.MatchesFile(path) {
		return FileResult{Path: path, NewSource: source}, nil
	}

	// 1. Check if this file is a dependency manifest (`go.mod`, `package.json`, `requirements.txt`, `pyproject.toml`).
	if e.manifestUpgrader != nil && len(recipe.Manifests) > 0 {
		current := source
		editsCount := 0
		for _, mRule := range recipe.Manifests {
			updated, changed := e.manifestUpgrader(path, current, mRule)
			if changed {
				current = updated
				editsCount++
			}
		}
		if editsCount > 0 {
			return FileResult{
				Path:         path,
				Language:     "manifest",
				Changed:      true,
				EditsApplied: editsCount,
				Diff:         UnifiedDiff(path, source, current),
				NewSource:    current,
			}, nil
		}
	}

	// 2. Check if a structured config/infrastructure rewriter (YAML, .properties, Dockerfile) handles this file.
	if e.configRewriter != nil && len(recipe.Rules) > 0 {
		updated, editsCount, err := e.configRewriter(path, source, recipe.Rules)
		if err != nil {
			return FileResult{}, err
		}
		if editsCount > 0 {
			return FileResult{
				Path:         path,
				Language:     "config",
				Changed:      true,
				EditsApplied: editsCount,
				Diff:         UnifiedDiff(path, source, updated),
				NewSource:    updated,
			}, nil
		}
	}

	// 3. Check if a LanguageSupport provider handles this file extension.
	ext := strings.ToLower(filepath.Ext(path))
	provider, ok := e.byExt[ext]
	if !ok {
		return FileResult{Path: path, NewSource: source}, nil
	}

	// Filter rules applicable to this language and file path preconditions.
	var langRules []RewriteRule
	for _, r := range recipe.Rules {
		if normalizeLangName(r.Language) == provider.Name() && r.Preconditions.MatchesFile(path) {
			langRules = append(langRules, r)
		}
	}
	if len(langRules) == 0 {
		return FileResult{Path: path, Language: provider.Name(), NewSource: source}, nil
	}

	doc, err := NewDocument(path, provider.Name(), provider.Language(), source)
	if err != nil {
		return FileResult{}, err
	}
	defer doc.Close()

	// Capture initial intra-file scope before rules mutate imports/constructors,
	// and also refresh scope between passes so newly introduced variables are tracked.
	initialScope := provider.AnalyzeScope(doc)
	totalEdits := 0
	var emittedRows []DataTableRow

	var pendingAddImports []ImportSpec
	var pendingRemoveImports []string

	for _, rule := range langRules {
		compiledRule, err := CompileRuleConstraint(
			rule.RootConstraint(),
			provider.Name(),
			provider.Language(),
			provider.ExpressionWrappers(),
		)
		if err != nil {
			return FileResult{}, fmt.Errorf("rule %q: %w", rule.ID, err)
		}

		currentScope := provider.AnalyzeScope(doc)
		matches := compiledRule.FindMatches(doc)
		var ruleEdits []Edit

		for _, m := range matches {
			if rule.FromPackage != "" {
				targetVar := rule.TargetVar
				if targetVar != "" {
					capVal, exists := m.Captures[targetVar]
					if !exists {
						continue
					}
					if !currentScope.SymbolBelongsToPackage(capVal.Text, rule.FromPackage) &&
						!initialScope.SymbolBelongsToPackage(capVal.Text, rule.FromPackage) {
						continue
					}
				} else if !anyCaptureOrScopeMatchesPackage(m.Captures, currentScope, initialScope, rule.FromPackage) {
					continue
				}
			}

			// Emit structured DataTable row if rule specifies `dataTable`
			if rule.DataTable != nil {
				row := buildDataTableRow(path, rule, m.Node, doc.Source, m.Captures)
				emittedRows = append(emittedRows, row)
			}

			// If rule has a non-empty `replace`, queue a code rewrite Edit
			if rule.Replace != "" {
				repl, err := RenderReplacementWithTransforms(
					rule.Replace,
					m.Node.Utf8Text(doc.Source),
					m.Captures,
					rule.Transforms,
				)
				if err != nil {
					compiledRule.Close()
					return FileResult{}, fmt.Errorf("rule %q transform: %w", rule.ID, err)
				}
				ruleEdits = append(ruleEdits, doc.NodeEdit(m.Node, repl, rule.ID))
			}
		}
		compiledRule.Close()

		if len(ruleEdits) > 0 {
			applied, err := doc.ApplyEdits(ruleEdits)
			if err != nil {
				return FileResult{}, err
			}
			totalEdits += applied
			if applied > 0 {
				pendingAddImports = append(pendingAddImports, rule.AddImports...)
				pendingRemoveImports = append(pendingRemoveImports, rule.RemoveImports...)
			}
		}
	}

	// Apply queued import additions & removals incrementally.
	for _, imp := range pendingAddImports {
		if edit := provider.AddImport(doc, imp.Module, imp.Symbol); edit != nil {
			applied, err := doc.ApplyEdits([]Edit{*edit})
			if err != nil {
				return FileResult{}, err
			}
			totalEdits += applied
		}
	}
	for _, mod := range pendingRemoveImports {
		if edit := provider.RemoveImport(doc, mod); edit != nil {
			applied, err := doc.ApplyEdits([]Edit{*edit})
			if err != nil {
				return FileResult{}, err
			}
			totalEdits += applied
		}
	}

	changed := !bytes.Equal(source, doc.Source)
	return FileResult{
		Path:         path,
		Language:     provider.Name(),
		Changed:      changed,
		EditsApplied: totalEdits,
		Diff:         UnifiedDiff(path, source, doc.Source),
		Rows:         emittedRows,
		NewSource:    doc.Source,
	}, nil
}

// RunOnDirectory walks rootDir, applies recipe to all matching files, and optionally writes changes to disk.
func (e *Engine) RunOnDirectory(rootDir string, recipe *RecipeSpec, writeInPlace bool) (*RunReport, error) {
	return e.RunWithOptions(rootDir, recipe, RunOptions{WriteInPlace: writeInPlace})
}

// RunOnDirectoryWithOptions walks rootDir and executes recipe. When auditOnly is true,
// DataTable rows and impact findings are collected without modifying files on disk.
func (e *Engine) RunOnDirectoryWithOptions(rootDir string, recipe *RecipeSpec, writeInPlace bool, auditOnly bool) (*RunReport, error) {
	return e.RunWithOptions(rootDir, recipe, RunOptions{
		WriteInPlace: writeInPlace,
		AuditOnly:    auditOnly,
	})
}

// RunWithOptions executes recipe across rootDir, applying in-place edits, running any configured
// build/test verification hooks (`verify.commands` or `opts.VerifyCommands`), and automatically
// rolling back modified files on disk if verification fails and rollback is enabled.
func (e *Engine) RunWithOptions(rootDir string, recipe *RecipeSpec, opts RunOptions) (*RunReport, error) {
	report := &RunReport{
		RecipeName: recipe.Name,
		DataTables: make(map[string][]DataTableRow),
	}

	// Verify repository-level preconditions (e.g. `requiredFiles: ["pom.xml"]`).
	if !recipe.Preconditions.MatchesRepo(rootDir) {
		return report, nil
	}

	// Track original file contents for atomic rollback if verification fails.
	backups := make(map[string][]byte)

	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}

		if !e.isCandidateFile(path) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		report.FilesScanned++

		relPath, relErr := filepath.Rel(rootDir, path)
		if relErr != nil || relPath == "." {
			relPath = path
		}

		res, err := e.RunOnBytes(relPath, data, recipe)
		if err != nil {
			return fmt.Errorf("processing %s: %w", relPath, err)
		}

		for _, row := range res.Rows {
			report.DataTables[row.Table] = append(report.DataTables[row.Table], row)
		}

		if res.Changed || len(res.Rows) > 0 {
			if res.Changed {
				report.FilesChanged++
			}
			report.Results = append(report.Results, res)
			if opts.WriteInPlace && !opts.AuditOnly && res.Changed {
				backups[path] = data
				if err := os.WriteFile(path, res.NewSource, 0o644); err != nil {
					return fmt.Errorf("writing %s: %w", path, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	report.Impact = report.ComputeImpactSummary()

	// Determine effective verification commands (CLI overrides or recipe.Verify.Commands)
	var verifyCmds []string
	timeoutSec := opts.TimeoutSeconds
	shouldRollback := true
	if recipe.Verify != nil {
		verifyCmds = append(verifyCmds, recipe.Verify.Commands...)
		if timeoutSec <= 0 {
			timeoutSec = recipe.Verify.TimeoutSeconds
		}
		shouldRollback = recipe.Verify.ShouldRollback()
	}
	if len(opts.VerifyCommands) > 0 {
		verifyCmds = opts.VerifyCommands
	}
	if opts.RollbackOnFailure != nil {
		shouldRollback = *opts.RollbackOnFailure
	}

	if opts.WriteInPlace && !opts.AuditOnly && report.FilesChanged > 0 && len(verifyCmds) > 0 {
		verResult := runVerificationCommands(rootDir, verifyCmds, timeoutSec)
		if !verResult.Passed && shouldRollback {
			for diskPath, origBytes := range backups {
				_ = os.WriteFile(diskPath, origBytes, 0o644)
			}
			verResult.RolledBack = true
		}
		report.Verification = verResult
	}

	return report, nil
}

func (e *Engine) isCandidateFile(path string) bool {
	base := filepath.Base(path)
	lowerBase := strings.ToLower(base)
	switch base {
	case "go.mod", "package.json", "requirements.txt", "pyproject.toml", "Cargo.toml", "pom.xml", "Gemfile":
		return true
	}
	if lowerBase == "dockerfile" || strings.HasPrefix(lowerBase, "dockerfile.") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml", ".properties", ".env", ".ini", ".cfg", ".dockerfile":
		return true
	}
	_, ok := e.byExt[ext]
	return ok
}

func anyCaptureOrScopeMatchesPackage(
	captures map[string]Capture,
	currentScope ScopeVerifier,
	initialScope ScopeVerifier,
	targetPkg string,
) bool {
	for _, c := range captures {
		if currentScope.SymbolBelongsToPackage(c.Text, targetPkg) ||
			initialScope.SymbolBelongsToPackage(c.Text, targetPkg) {
			return true
		}
	}
	return currentScope.HasPackage(targetPkg) || initialScope.HasPackage(targetPkg)
}

func normalizeLangName(name string) string {
	norm := strings.ToLower(strings.TrimSpace(name))
	switch norm {
	case "golang":
		return "go"
	case "ts", "js", "javascript", "tsx":
		return "typescript"
	case "py":
		return "python"
	case "rs":
		return "rust"
	case "rb":
		return "ruby"
	case "c++", "cxx", "cc", "hpp":
		return "cpp"
	case "xml", "htm", "svg":
		return "html"
	case "yml", "k8s", "kubernetes", "github-actions":
		return "yaml"
	case "props", "dotenv", "env", "ini":
		return "properties"
	case "docker", "containerfile":
		return "dockerfile"
	default:
		return norm
	}
}
