// Command code-graft is a polyglot Tree-sitter code rewriting CLI inspired by OpenRewrite.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/loganprice/code-graft/internal/config"
	clang "github.com/loganprice/code-graft/internal/lang/c"
	"github.com/loganprice/code-graft/internal/lang/cpp"
	"github.com/loganprice/code-graft/internal/lang/golang"
	"github.com/loganprice/code-graft/internal/lang/html"
	"github.com/loganprice/code-graft/internal/lang/java"
	jsonlang "github.com/loganprice/code-graft/internal/lang/json"
	"github.com/loganprice/code-graft/internal/lang/python"
	"github.com/loganprice/code-graft/internal/lang/ruby"
	"github.com/loganprice/code-graft/internal/lang/rust"
	"github.com/loganprice/code-graft/internal/lang/typescript"
	"github.com/loganprice/code-graft/internal/manifest"
	"github.com/loganprice/code-graft/pkg/rewrite"
)

type paramMapFlag map[string]string

func (p *paramMapFlag) String() string {
	return fmt.Sprintf("%v", *p)
}

func (p *paramMapFlag) Set(val string) error {
	idx := strings.Index(val, "=")
	if idx <= 0 {
		return fmt.Errorf("invalid --param %q (expected KEY=VALUE)", val)
	}
	if *p == nil {
		*p = make(map[string]string)
	}
	(*p)[strings.TrimSpace(val[:idx])] = val[idx+1:]
	return nil
}

func main() {
	cliParams := make(paramMapFlag)
	var (
		recipePath   = flag.String("recipe", "", "Path to YAML recipe file (required except with --renovate-upgrade)")
		targetDir    = flag.String("dir", ".", "Target workspace directory to scan and rewrite")
		writeFlag    = flag.Bool("write", false, "Write changes in-place to disk (defaults to dry-run)")
		dryRunFlag   = flag.Bool("dry-run", true, "Print unified diffs without modifying files on disk")
		auditFlag    = flag.Bool("audit", false, "Run in audit/impact-analysis mode (collect DataTables without writing files)")
		validateFlag = flag.Bool("validate", false, "Validate recipe governance, syntax, and rule IDs without running on files")
		testFlag     = flag.Bool("test", false, "Validate recipe and run embedded recipe unit tests")
		jsonFlag     = flag.Bool("json", false, "Output structured JSON report")
		csvFlag      = flag.Bool("csv", false, "Output emitted DataTables as CSV")
		markdownFlag = flag.Bool("markdown", false, "Output Markdown Impact Analysis & DataTable report (for Renovate PRs / GitHub Actions)")
		verifyCmd    = flag.String("verify-cmd", "", "Shell command to run in --dir after applying edits (automatically rolls back on failure unless --no-rollback)")
		noRollback   = flag.Bool("no-rollback", false, "Keep modified files on disk even if --verify-cmd or recipe.verify fails")
		renovateFlag = flag.String("renovate-upgrade", "", "Auto-discover recipes in --catalog-dir matching '<depName>@<newVer>' for Renovate postUpgradeTasks")
		catalogDir   = flag.String("catalog-dir", "configs/examples", "Directory of YAML recipes to search when using --renovate-upgrade")
		learnBefore  = flag.String("learn-before", "", "Path to 'Before' source file for By-Example Recipe Generator")
		learnAfter   = flag.String("learn-after", "", "Path to 'After' source file for By-Example Recipe Generator")
		learnCommit  = flag.String("learn-commit", "", "Git commit ref (e.g. HEAD) to learn a YAML recipe from")
		learnOut     = flag.String("learn-out", "", "Optional output file path to write the learned YAML recipe (defaults to stdout)")
		learnName    = flag.String("learn-name", "learned-migration-recipe", "Recipe name when generating a recipe with --learn-before/--learn-commit")
	)
	flag.Var(&cliParams, "param", "Template parameter override in KEY=VALUE format (repeatable)")
	flag.Parse()

	engine := newDefaultEngine()

	// Mode 1: By-Example Recipe Generator (`--learn-before` / `--learn-after` or `--learn-commit`)
	if *learnBefore != "" || *learnCommit != "" {
		var learnedSpec *rewrite.RecipeSpec
		var err error
		if *learnCommit != "" {
			learnedSpec, _, err = engine.LearnFromGitCommit(*targetDir, *learnCommit, *learnName)
		} else {
			if *learnAfter == "" {
				fmt.Fprintln(os.Stderr, "error: --learn-after <path> is required when using --learn-before")
				os.Exit(2)
			}
			beforeBytes, err1 := os.ReadFile(*learnBefore)
			afterBytes, err2 := os.ReadFile(*learnAfter)
			if err1 != nil || err2 != nil {
				fmt.Fprintf(os.Stderr, "error reading learn files: %v / %v\n", err1, err2)
				os.Exit(1)
			}
			learnedSpec, _, err = engine.LearnFromExamples(*learnName, []rewrite.LearnExample{
				{
					Path:   *learnBefore,
					Before: beforeBytes,
					After:  afterBytes,
				},
			})
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error learning recipe: %v\n", err)
			os.Exit(1)
		}

		yamlBytes, err := rewrite.FormatRecipeYAML(learnedSpec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error marshaling learned recipe: %v\n", err)
			os.Exit(1)
		}
		if *learnOut != "" {
			if err := os.WriteFile(*learnOut, yamlBytes, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "error writing %s: %v\n", *learnOut, err)
				os.Exit(1)
			}
		} else {
			fmt.Print(string(yamlBytes))
		}
		return
	}

	var spec *rewrite.RecipeSpec
	var err error
	if *renovateFlag != "" {
		spec, err = rewrite.DiscoverRenovateRecipe(*catalogDir, *renovateFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error discovering Renovate recipe for %q: %v\n", *renovateFlag, err)
			os.Exit(1)
		}
	} else {
		if *recipePath == "" {
			fmt.Fprintln(os.Stderr, "error: --recipe <path.yaml> or --renovate-upgrade <dep@ver> is required")
			flag.Usage()
			os.Exit(2)
		}
		spec, err = rewrite.LoadRecipeYAMLWithParams(*recipePath, cliParams)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error loading recipe: %v\n", err)
			os.Exit(1)
		}
	}

	if *validateFlag || *testFlag {
		var govReport *rewrite.GovernanceReport
		if *testFlag {
			govReport = engine.RunRecipeTests(spec)
		} else {
			govReport = engine.ValidateRecipe(spec)
		}

		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(govReport)
		} else {
			fmt.Printf("Governance Check: %s (v%s) — Valid: %v, Tests: %d/%d passed\n",
				govReport.RecipeName, govReport.Version, govReport.Valid, govReport.TestsPassed, govReport.TestsRun)
			for _, issue := range govReport.Issues {
				fmt.Printf("  [%s] %s: %s\n", issue.Severity, issue.RuleID, issue.Message)
			}
			for _, tr := range govReport.TestResults {
				if !tr.Passed {
					fmt.Printf("  [FAIL] %s (%s)\n%s\n", tr.Name, tr.Path, tr.Diff)
				} else {
					fmt.Printf("  [PASS] %s (%s)\n", tr.Name, tr.Path)
				}
			}
		}
		if !govReport.Valid {
			os.Exit(1)
		}
		return
	}

	// If --write is explicitly passed, disable dry-run.
	writeInPlace := *writeFlag
	_ = *dryRunFlag

	runOpts := rewrite.RunOptions{
		WriteInPlace: writeInPlace,
		AuditOnly:    *auditFlag,
	}
	if *verifyCmd != "" {
		runOpts.VerifyCommands = []string{*verifyCmd}
	}
	if *noRollback {
		rb := false
		runOpts.RollbackOnFailure = &rb
	}

	report, err := engine.RunWithOptions(*targetDir, spec, runOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error running recipe: %v\n", err)
		os.Exit(1)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "error encoding JSON report: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *csvFlag {
		csvOut, err := report.FormatDataTablesCSV()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error formatting CSV report: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(csvOut)
		return
	}

	if *markdownFlag {
		fmt.Print(report.FormatMarkdownSummary())
		return
	}

	fmt.Printf("Recipe: %s (%d files scanned, %d files modified, %d data table findings)\n",
		report.RecipeName, report.FilesScanned, report.FilesChanged, report.Impact.TotalFindings)
	if report.Verification != nil && report.Verification.Attempted {
		fmt.Printf("Verification: passed=%v, rolledBack=%v (%d diagnostics)\n",
			report.Verification.Passed, report.Verification.RolledBack, len(report.Verification.Diagnostics))
	}
	for _, res := range report.Results {
		if res.Diff != "" {
			fmt.Println(res.Diff)
		}
	}
	if report.Verification != nil && report.Verification.Attempted && !report.Verification.Passed {
		os.Exit(1)
	}
}

func newDefaultEngine() *rewrite.Engine {
	upgrader := func(filePath string, content []byte, rule rewrite.ManifestRule) ([]byte, bool) {
		return manifest.ApplyUpgrade(filePath, content, manifest.DependencyUpgrade{
			Ecosystem:  rule.Ecosystem,
			OldPackage: rule.OldPackage,
			NewPackage: rule.NewPackage,
			NewVersion: rule.NewVersion,
		})
	}
	eng := rewrite.NewEngine(
		upgrader,
		golang.NewProvider(),
		typescript.NewProvider(),
		python.NewProvider(),
		java.NewProvider(),
		rust.NewProvider(),
		ruby.NewProvider(),
		clang.NewProvider(),
		cpp.NewProvider(),
		jsonlang.NewProvider(),
		html.NewProvider(),
	)
	eng.SetConfigRewriter(config.ApplyConfigRules)
	return eng
}
