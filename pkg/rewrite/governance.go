package rewrite

import (
	"fmt"
	"strings"
)

// GovernanceIssue represents a single validation error or warning found during recipe governance linting.
type GovernanceIssue struct {
	Severity string `json:"severity"` // "error" or "warning"
	RuleID   string `json:"ruleId,omitempty"`
	Message  string `json:"message"`
}

// TestCaseResult records the pass/fail status of an embedded RecipeTestSuite case.
type TestCaseResult struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Passed bool   `json:"passed"`
	Diff   string `json:"diff,omitempty"`
	Error  string `json:"error,omitempty"`
}

// GovernanceReport summarizes the validation and embedded self-test results for a RecipeSpec.
type GovernanceReport struct {
	RecipeName  string            `json:"recipeName"`
	Version     string            `json:"version,omitempty"`
	Valid       bool              `json:"valid"`
	Issues      []GovernanceIssue `json:"issues,omitempty"`
	TestsRun    int               `json:"testsRun"`
	TestsPassed int               `json:"testsPassed"`
	TestResults []TestCaseResult  `json:"testResults,omitempty"`
}

// ValidateRecipe performs enterprise governance checks on a RecipeSpec:
//   - Verifies recipe metadata (name, description, version/category warnings).
//   - Verifies unique rule IDs across composed rules.
//   - Compiles every rule's pattern, combinator tree, regexes, and metavariable transforms
//     against its target language grammar to catch syntax errors before deployment.
func (e *Engine) ValidateRecipe(recipe *RecipeSpec) *GovernanceReport {
	report := &GovernanceReport{
		RecipeName: recipe.Name,
		Version:    recipe.Version,
		Valid:      true,
	}

	if strings.TrimSpace(recipe.Description) == "" {
		report.Issues = append(report.Issues, GovernanceIssue{
			Severity: "warning",
			Message:  "recipe is missing a 'description'",
		})
	}
	if strings.TrimSpace(recipe.Version) == "" {
		report.Issues = append(report.Issues, GovernanceIssue{
			Severity: "warning",
			Message:  "recipe is missing a semantic 'version'",
		})
	}
	if len(recipe.Rules) == 0 && len(recipe.Manifests) == 0 {
		report.Valid = false
		report.Issues = append(report.Issues, GovernanceIssue{
			Severity: "error",
			Message:  "recipe contains neither rules nor manifest upgrades",
		})
	}

	seenIDs := make(map[string]bool)
	for i, rule := range recipe.Rules {
		ruleLabel := rule.ID
		if ruleLabel == "" {
			ruleLabel = fmt.Sprintf("rule[%d]", i)
			report.Issues = append(report.Issues, GovernanceIssue{
				Severity: "warning",
				RuleID:   ruleLabel,
				Message:  "rule is missing a unique 'id'",
			})
		} else if seenIDs[rule.ID] {
			report.Valid = false
			report.Issues = append(report.Issues, GovernanceIssue{
				Severity: "error",
				RuleID:   rule.ID,
				Message:  fmt.Sprintf("duplicate rule id %q", rule.ID),
			})
		} else {
			seenIDs[rule.ID] = true
		}

		langNorm := normalizeLangName(rule.Language)
		if langNorm == "yaml" || langNorm == "properties" || langNorm == "dockerfile" {
			// Config/infrastructure rules are handled by the structured ConfigRewriter.
			continue
		}
		provider, ok := e.byName[langNorm]
		if !ok {
			report.Valid = false
			report.Issues = append(report.Issues, GovernanceIssue{
				Severity: "error",
				RuleID:   ruleLabel,
				Message:  fmt.Sprintf("unsupported language %q", rule.Language),
			})
			continue
		}

		compiled, err := CompileRuleConstraint(
			rule.RootConstraint(),
			provider.Name(),
			provider.Language(),
			provider.ExpressionWrappers(),
		)
		if err != nil {
			report.Valid = false
			report.Issues = append(report.Issues, GovernanceIssue{
				Severity: "error",
				RuleID:   ruleLabel,
				Message:  fmt.Sprintf("failed to compile rule constraint: %v", err),
			})
			continue
		}
		compiled.Close()
	}

	return report
}

// RunRecipeTests validates the recipe and executes all embedded `tests` cases in-memory.
func (e *Engine) RunRecipeTests(recipe *RecipeSpec) *GovernanceReport {
	report := e.ValidateRecipe(recipe)
	for _, tc := range recipe.Tests {
		report.TestsRun++
		res, err := e.RunOnBytes(tc.Path, []byte(tc.Input), recipe)
		if err != nil {
			report.Valid = false
			report.TestResults = append(report.TestResults, TestCaseResult{
				Name:   tc.Name,
				Path:   tc.Path,
				Passed: false,
				Error:  err.Error(),
			})
			continue
		}

		expected := tc.Expected
		if expected == "" {
			expected = tc.Input
		}
		actual := string(res.NewSource)
		if actual == expected {
			report.TestsPassed++
			report.TestResults = append(report.TestResults, TestCaseResult{
				Name:   tc.Name,
				Path:   tc.Path,
				Passed: true,
			})
		} else {
			report.Valid = false
			report.TestResults = append(report.TestResults, TestCaseResult{
				Name:   tc.Name,
				Path:   tc.Path,
				Passed: false,
				Diff:   UnifiedDiff(tc.Path, []byte(expected), []byte(actual)),
			})
		}
	}
	return report
}
