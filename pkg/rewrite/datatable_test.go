package rewrite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDataTablesImpactAnalysisAndRenovateIntegration(t *testing.T) {
	engine := newTestEngine()

	t.Run("search-only and hybrid DataTable emission with CSV and Markdown export", func(t *testing.T) {
		recipeYAML := `
name: "audit-and-migrate-acme-sdk"
version: "2.3.0"
description: "Emits DataTable rows for every deprecated call site and rewrites them"
rules:
  - id: "go-audit-and-rewrite-get-user"
    language: "go"
    match: "$CLIENT.GetUser($CTX, $ID)"
    replace: "$CLIENT.FetchUser($CTX, $ID, nil)"
    targetVar: "CLIENT"
    fromPackage: "github.com/acme/sdk-go"
    dataTable:
      name: "acme_sdk_blast_radius"
      columns:
        clientVar: "$CLIENT"
        userIdArg: "$ID"
        package: "$PACKAGE"
        location: "$FILE:$LINE"

  - id: "go-search-only-hardcoded-token"
    language: "go"
    match: "acmesdk.NewClient($TOKEN)"
    fromPackage: "github.com/acme/sdk-go"
    where:
      TOKEN: '^".*"$'
    dataTable:
      name: "security_hardcoded_tokens"
      columns:
        tokenLiteral: "$TOKEN"
`
		recipe, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
		require.NoError(t, err)

		tmpDir := t.TempDir()
		goSrc := `package main

import (
	"context"
	acmesdk "github.com/acme/sdk-go"
)

func run(ctx context.Context) {
	c := acmesdk.NewClient("hardcoded-secret-123")
	c.GetUser(ctx, "usr_1")
	c.GetUser(ctx, "usr_2")
}
`
		filePath := filepath.Join(tmpDir, "main.go")
		require.NoError(t, os.WriteFile(filePath, []byte(goSrc), 0o644))

		// 1. Run in auditOnly=true mode first: collects DataTables & Impact Summary without modifying disk
		auditReport, err := engine.RunOnDirectoryWithOptions(tmpDir, recipe, true, true)
		require.NoError(t, err)
		assert.Equal(t, 3, auditReport.Impact.TotalFindings)
		assert.Equal(t, 1, auditReport.Impact.AffectedFiles)
		assert.Len(t, auditReport.DataTables["acme_sdk_blast_radius"], 2)
		assert.Len(t, auditReport.DataTables["security_hardcoded_tokens"], 1)

		// Verify file on disk was NOT modified during auditOnly mode
		diskBytes, err := os.ReadFile(filePath)
		require.NoError(t, err)
		assert.Equal(t, goSrc, string(diskBytes))

		// Verify CSV export
		csvOut, err := auditReport.FormatDataTablesCSV()
		require.NoError(t, err)
		assert.Contains(t, csvOut, "acme_sdk_blast_radius,main.go,10")
		assert.Contains(t, csvOut, "security_hardcoded_tokens,main.go,9")
		assert.Contains(t, csvOut, `"hardcoded-secret-123"`)

		// Verify Markdown summary export for Renovate PR comments
		mdOut := auditReport.FormatMarkdownSummary()
		assert.Contains(t, mdOut, "## 🔍 `code-graft` Impact Analysis: `audit-and-migrate-acme-sdk`")
		assert.Contains(t, mdOut, "### 📊 Data Table: `acme_sdk_blast_radius` (2 rows)")
		assert.Contains(t, mdOut, "### 📊 Data Table: `security_hardcoded_tokens` (1 rows)")
	})

	t.Run("Renovate auto-discovery (--renovate-upgrade dep@ver)", func(t *testing.T) {
		catalogDir := filepath.Join("..", "..", "configs", "examples")
		spec, err := rewrite.DiscoverRenovateRecipe(catalogDir, "github.com/acme/sdk-go@v2.5.0")
		require.NoError(t, err)
		assert.Equal(t, "renovate-auto-migration(github.com/acme/sdk-go@v2.5.0)", spec.Name)
		assert.NotEmpty(t, spec.Rules)

		// Verify the manifest rule's NewVersion was dynamically overridden to v2.5.0 from Renovate
		foundOverride := false
		for _, m := range spec.Manifests {
			if m.OldPackage == "github.com/acme/sdk-go" && m.NewVersion == "v2.5.0" {
				foundOverride = true
			}
		}
		assert.True(t, foundOverride, "expected Renovate newVersion v2.5.0 to override manifest rule")
	})
}
