package rewrite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAndTestVerificationHook(t *testing.T) {
	engine := newTestEngine()

	t.Run("verification passes and keeps rewritten files on disk", func(t *testing.T) {
		recipeYAML := `
name: "verified-go-migration"
version: "1.0.0"
description: "Rewrites legacyCall and verifies with a shell command"
verify:
  commands:
    - "grep -q 'modernCall' main.go"
rules:
  - id: "upgrade-call"
    language: "go"
    match: "legacyCall()"
    replace: "modernCall()"
`
		recipe, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
		require.NoError(t, err)

		tmpDir := t.TempDir()
		mainPath := filepath.Join(tmpDir, "main.go")
		require.NoError(t, os.WriteFile(mainPath, []byte("package main\nfunc main() { legacyCall() }\n"), 0o644))

		report, err := engine.RunOnDirectory(tmpDir, recipe, true)
		require.NoError(t, err)
		require.NotNil(t, report.Verification)
		assert.True(t, report.Verification.Attempted)
		assert.True(t, report.Verification.Passed)
		assert.False(t, report.Verification.RolledBack)

		diskContent, err := os.ReadFile(mainPath)
		require.NoError(t, err)
		assert.Contains(t, string(diskContent), "modernCall()")
	})

	t.Run("verification fails, parses compiler diagnostics, and rolls back files atomically", func(t *testing.T) {
		recipeYAML := `
name: "failing-verification-rollback"
version: "1.0.0"
description: "Simulates a broken migration where compiler verification fails and triggers rollback"
verify:
  commands:
    - "echo 'main.go:14:9: undefined: brokenSymbol' && exit 1"
rules:
  - id: "introduce-broken-call"
    language: "go"
    match: "legacyCall()"
    replace: "brokenSymbol()"
`
		recipe, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
		require.NoError(t, err)

		tmpDir := t.TempDir()
		mainPath := filepath.Join(tmpDir, "main.go")
		origSrc := "package main\nfunc main() { legacyCall() }\n"
		require.NoError(t, os.WriteFile(mainPath, []byte(origSrc), 0o644))

		report, err := engine.RunOnDirectory(tmpDir, recipe, true)
		require.NoError(t, err)
		require.NotNil(t, report.Verification)
		assert.True(t, report.Verification.Attempted)
		assert.False(t, report.Verification.Passed)
		assert.True(t, report.Verification.RolledBack)

		// Verify compiler diagnostic was extracted
		require.Len(t, report.Verification.Diagnostics, 1)
		assert.Equal(t, "main.go", report.Verification.Diagnostics[0].File)
		assert.Equal(t, 14, report.Verification.Diagnostics[0].Line)
		assert.Equal(t, 9, report.Verification.Diagnostics[0].Column)
		assert.Equal(t, "undefined: brokenSymbol", report.Verification.Diagnostics[0].Message)

		// Verify file on disk was automatically rolled back to origSrc
		diskContent, err := os.ReadFile(mainPath)
		require.NoError(t, err)
		assert.Equal(t, origSrc, string(diskContent))

		// Verify Markdown summary includes the verification status and diagnostic table
		md := report.FormatMarkdownSummary()
		assert.Contains(t, md, "❌ Failed (Changes Rolled Back)")
		assert.Contains(t, md, "### ⚠️ Verification Compiler Diagnostics")
		assert.Contains(t, md, "undefined: brokenSymbol")
	})

	t.Run("rollbackOnFailure=false keeps modified files on disk for LLM / human inspection", func(t *testing.T) {
		recipeYAML := `
name: "keep-on-failure"
version: "1.0.0"
description: "Keeps broken changes when rollbackOnFailure is false"
rules:
  - id: "introduce-broken-call"
    language: "go"
    match: "legacyCall()"
    replace: "brokenSymbol()"
`
		recipe, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
		require.NoError(t, err)

		tmpDir := t.TempDir()
		mainPath := filepath.Join(tmpDir, "main.go")
		require.NoError(t, os.WriteFile(mainPath, []byte("package main\nfunc main() { legacyCall() }\n"), 0o644))

		noRollback := false
		report, err := engine.RunWithOptions(tmpDir, recipe, rewrite.RunOptions{
			WriteInPlace:      true,
			VerifyCommands:    []string{"echo '[ERROR] /app/Order.java:[42,10] cannot find symbol' && exit 1"},
			RollbackOnFailure: &noRollback,
		})
		require.NoError(t, err)
		require.NotNil(t, report.Verification)
		assert.False(t, report.Verification.Passed)
		assert.False(t, report.Verification.RolledBack)
		require.Len(t, report.Verification.Diagnostics, 1)
		assert.Equal(t, "/app/Order.java", report.Verification.Diagnostics[0].File)
		assert.Equal(t, 42, report.Verification.Diagnostics[0].Line)

		diskContent, err := os.ReadFile(mainPath)
		require.NoError(t, err)
		assert.Contains(t, string(diskContent), "brokenSymbol()")
	})
}
