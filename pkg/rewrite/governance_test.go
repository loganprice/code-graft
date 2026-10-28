package rewrite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecipeCompositionPreconditionsAndGovernance(t *testing.T) {
	engine := newTestEngine()

	t.Run("recursive recipe composition via includes", func(t *testing.T) {
		bundlePath := filepath.Join("..", "..", "configs", "examples", "enterprise_modernization_bundle.yaml")
		bundle, err := rewrite.LoadRecipeYAML(bundlePath)
		require.NoError(t, err)

		assert.Equal(t, "enterprise-2026-modernization-bundle", bundle.Name)
		assert.Equal(t, "2026.1.0", bundle.Version)
		// Composed rules from both acme_sdk_v2.yaml (11 rules) and spring_boot_2_to_3.yaml (8 rules)
		assert.Equal(t, 19, len(bundle.Rules))
		assert.Equal(t, 4, len(bundle.Manifests))
		assert.Equal(t, 2, len(bundle.Tests))

		govReport := engine.RunRecipeTests(bundle)
		assert.True(t, govReport.Valid, "expected bundle governance check & embedded tests to pass: %+v", govReport)
		assert.Equal(t, 2, govReport.TestsRun)
		assert.Equal(t, 2, govReport.TestsPassed)
	})

	t.Run("cyclic recipe include detection", func(t *testing.T) {
		tmpDir := t.TempDir()
		aPath := filepath.Join(tmpDir, "a.yaml")
		bPath := filepath.Join(tmpDir, "b.yaml")

		require.NoError(t, os.WriteFile(aPath, []byte("name: a\nincludes:\n  - ./b.yaml\n"), 0o644))
		require.NoError(t, os.WriteFile(bPath, []byte("name: b\nincludes:\n  - ./a.yaml\n"), 0o644))

		_, err := rewrite.LoadRecipeYAML(aPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cyclic recipe include detected")
	})

	t.Run("repo-level and file-level preconditions", func(t *testing.T) {
		recipeYAML := `
name: "precondition-guarded-recipe"
version: "1.0.0"
description: "Only runs in repos with go.mod and skips generated or test files"
preconditions:
  requiredFiles:
    - "go.mod"
  filePatterns:
    - "**/*.go"
  excludeFiles:
    - "**/*_test.go"
    - "**/generated/**"
rules:
  - id: "upgrade-legacy-call"
    language: "go"
    match: "legacyCall()"
    replace: "modernCall()"
`
		spec, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
		require.NoError(t, err)

		// 1. Repo without go.mod -> skipped completely
		noGoModDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(noGoModDir, "main.go"), []byte("package main\nfunc f() { legacyCall() }\n"), 0o644))
		rep1, err := engine.RunOnDirectory(noGoModDir, spec, true)
		require.NoError(t, err)
		assert.Equal(t, 0, rep1.FilesScanned)
		assert.Equal(t, 0, rep1.FilesChanged)

		// 2. Repo with go.mod -> rewrites main.go, but skips main_test.go and generated/gen.go
		withGoModDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(withGoModDir, "go.mod"), []byte("module example.com/m\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(withGoModDir, "main.go"), []byte("package main\nfunc f() { legacyCall() }\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(withGoModDir, "main_test.go"), []byte("package main\nfunc t() { legacyCall() }\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(withGoModDir, "generated"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(withGoModDir, "generated", "gen.go"), []byte("package generated\nfunc g() { legacyCall() }\n"), 0o644))

		rep2, err := engine.RunOnDirectory(withGoModDir, spec, true)
		require.NoError(t, err)
		assert.Equal(t, 1, rep2.FilesChanged)

		mainBytes, _ := os.ReadFile(filepath.Join(withGoModDir, "main.go"))
		testBytes, _ := os.ReadFile(filepath.Join(withGoModDir, "main_test.go"))
		genBytes, _ := os.ReadFile(filepath.Join(withGoModDir, "generated", "gen.go"))

		assert.Contains(t, string(mainBytes), "modernCall()")
		assert.Contains(t, string(testBytes), "legacyCall()")
		assert.Contains(t, string(genBytes), "legacyCall()")
	})

	t.Run("governance validator catches duplicate rule IDs and invalid syntax", func(t *testing.T) {
		badYAML := `
name: "invalid-recipe"
rules:
  - id: "dup-id"
    language: "go"
    match: "foo()"
    replace: "bar()"
  - id: "dup-id"
    language: "unknown-lang"
    match: "foo()"
    replace: "bar()"
`
		spec, err := rewrite.ParseRecipeYAML([]byte(badYAML))
		require.NoError(t, err)

		report := engine.ValidateRecipe(spec)
		assert.False(t, report.Valid)
		assert.GreaterOrEqual(t, len(report.Issues), 2)
	})

	t.Run("parameterized recipe templates with required/default parameters and parameterized includes", func(t *testing.T) {
		templatePath, err := filepath.Abs(filepath.Join("..", "..", "configs", "examples", "templates", "rename_sdk_method.yaml"))
		require.NoError(t, err)

		// 1. Loading template without required parameters should fail with a descriptive error
		_, err = rewrite.LoadRecipeYAML(templatePath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing required recipe parameter")

		// 2. Composing the same template multiple times via `includes` with different `with` arguments
		parentYAML := `
name: "multi-template-instantiation"
version: "1.0.0"
description: "Instantiates rename_sdk_method.yaml for both Go and TypeScript"
includes:
  - path: "` + filepath.ToSlash(templatePath) + `"
    with:
      LANG: "go"
      TARGET_PKG: "github.com/acme/billing"
      OLD_METHOD: "ChargeCard"
      NEW_METHOD: "ProcessPayment"
      NEW_PKG: "github.com/acme/billing/v2"
      IMPORT_ALIAS: "billing"
  - path: "` + filepath.ToSlash(templatePath) + `"
    with:
      LANG: "typescript"
      TARGET_PKG: "@acme/billing-v1"
      OLD_METHOD: "chargeCard"
      NEW_METHOD: "processPayment"
      NEW_PKG: "@acme/billing-v2"
      IMPORT_ALIAS: "BillingClient"
`
		tmpDir := t.TempDir()
		parentPath := filepath.Join(tmpDir, "parent.yaml")
		require.NoError(t, os.WriteFile(parentPath, []byte(parentYAML), 0o644))

		composed, err := rewrite.LoadRecipeYAML(parentPath)
		require.NoError(t, err)
		require.Len(t, composed.Rules, 2)

		// Verify governance check passes and rule IDs are unique
		gov := engine.ValidateRecipe(composed)
		assert.True(t, gov.Valid, "expected composed parameterized recipe to be valid: %+v", gov)

		// Verify Go template instantiation rewrites Go code
		goSrc := `package pay

import billing "github.com/acme/billing"

func checkout() {
	b := billing.New()
	b.ChargeCard("card_123", 5000)
}
`
		goRes, err := engine.RunOnBytes("pay.go", []byte(goSrc), composed)
		require.NoError(t, err)
		assert.True(t, goRes.Changed)
		assert.Contains(t, string(goRes.NewSource), `b.ProcessPayment("card_123", 5000)`)
		assert.Contains(t, string(goRes.NewSource), `billing "github.com/acme/billing/v2"`)

		// Verify TypeScript template instantiation rewrites TS code
		tsSrc := `import { BillingClient } from '@acme/billing-v1';
const b = new BillingClient();
b.chargeCard("card_123", 5000);
`
		tsRes, err := engine.RunOnBytes("pay.ts", []byte(tsSrc), composed)
		require.NoError(t, err)
		assert.True(t, tsRes.Changed)
		assert.Contains(t, string(tsRes.NewSource), `b.processPayment("card_123", 5000);`)
		assert.Contains(t, string(tsRes.NewSource), `import { BillingClient } from '@acme/billing-v2';`)
	})
}
