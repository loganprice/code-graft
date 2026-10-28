package rewrite

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// DataTableRow represents a single structured codebase intelligence record emitted when a rule matches.
type DataTableRow struct {
	Table   string            `json:"table"`
	File    string            `json:"file"`
	Line    uint              `json:"line"`
	Column  uint              `json:"column"`
	RuleID  string            `json:"ruleId"`
	Snippet string            `json:"snippet"`
	Values  map[string]string `json:"values,omitempty"`
}

// ImpactSummary provides an executive blast-radius breakdown for a recipe run or Renovate upgrade.
type ImpactSummary struct {
	TotalFindings  int            `json:"totalFindings"`
	AffectedFiles  int            `json:"affectedFiles"`
	FindingsByRule map[string]int `json:"findingsByRule"`
	FindingsByLang map[string]int `json:"findingsByLang"`
}

// buildDataTableRow constructs a DataTableRow for a matched CST node and expands any custom column templates.
func buildDataTableRow(
	filePath string,
	rule RewriteRule,
	node *tree_sitter.Node,
	source []byte,
	captures map[string]Capture,
) DataTableRow {
	startPos := node.StartPosition()
	lineNum := startPos.Row + 1
	colNum := startPos.Column + 1
	snippet := strings.TrimSpace(node.Utf8Text(source))

	tableName := "default_findings"
	if rule.DataTable != nil && strings.TrimSpace(rule.DataTable.Name) != "" {
		tableName = strings.TrimSpace(rule.DataTable.Name)
	}

	// Build full capture map including built-in metadata variables ($0, $FILE, $LINE, $COL, $RULE_ID, $PACKAGE).
	metaCaps := make(map[string]Capture, len(captures)+6)
	for k, v := range captures {
		metaCaps[k] = v
	}
	metaCaps["0"] = Capture{Name: "0", Text: snippet}
	metaCaps["FILE"] = Capture{Name: "FILE", Text: filePath}
	metaCaps["LINE"] = Capture{Name: "LINE", Text: strconv.FormatUint(uint64(lineNum), 10)}
	metaCaps["COL"] = Capture{Name: "COL", Text: strconv.FormatUint(uint64(colNum), 10)}
	metaCaps["RULE_ID"] = Capture{Name: "RULE_ID", Text: rule.ID}
	metaCaps["PACKAGE"] = Capture{Name: "PACKAGE", Text: rule.FromPackage}

	var values map[string]string
	if rule.DataTable != nil && len(rule.DataTable.Columns) > 0 {
		values = make(map[string]string, len(rule.DataTable.Columns))
		for colName, expr := range rule.DataTable.Columns {
			rendered, err := RenderReplacementWithTransforms(expr, snippet, metaCaps, rule.Transforms)
			if err == nil {
				values[colName] = string(rendered)
			} else {
				values[colName] = expr
			}
		}
	}

	return DataTableRow{
		Table:   tableName,
		File:    filePath,
		Line:    lineNum,
		Column:  colNum,
		RuleID:  rule.ID,
		Snippet: snippet,
		Values:  values,
	}
}

// ComputeImpactSummary aggregates blast-radius statistics across all results in a RunReport.
func (r *RunReport) ComputeImpactSummary() ImpactSummary {
	summary := ImpactSummary{
		FindingsByRule: make(map[string]int),
		FindingsByLang: make(map[string]int),
	}
	affectedFilesSet := make(map[string]bool)

	for _, res := range r.Results {
		if res.Changed || len(res.Rows) > 0 {
			affectedFilesSet[res.Path] = true
		}
		for _, row := range res.Rows {
			summary.TotalFindings++
			summary.FindingsByRule[row.RuleID]++
			summary.FindingsByLang[res.Language]++
		}
	}
	for _, rows := range r.DataTables {
		if summary.TotalFindings == 0 {
			summary.TotalFindings += len(rows)
		}
	}
	summary.AffectedFiles = len(affectedFilesSet)
	return summary
}

// FormatDataTablesCSV serializes all emitted DataTables in the RunReport into CSV format.
func (r *RunReport) FormatDataTablesCSV() (string, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	// Collect all dynamic column names in deterministic sorted order.
	colSet := make(map[string]bool)
	for _, rows := range r.DataTables {
		for _, row := range rows {
			for col := range row.Values {
				colSet[col] = true
			}
		}
	}
	customCols := make([]string, 0, len(colSet))
	for col := range colSet {
		customCols = append(customCols, col)
	}
	sort.Strings(customCols)

	header := append([]string{"table", "file", "line", "column", "rule_id", "snippet"}, customCols...)
	if err := w.Write(header); err != nil {
		return "", err
	}

	tableNames := make([]string, 0, len(r.DataTables))
	for name := range r.DataTables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)

	for _, tName := range tableNames {
		for _, row := range r.DataTables[tName] {
			record := []string{
				row.Table,
				row.File,
				strconv.FormatUint(uint64(row.Line), 10),
				strconv.FormatUint(uint64(row.Column), 10),
				row.RuleID,
				row.Snippet,
			}
			for _, col := range customCols {
				record = append(record, row.Values[col])
			}
			if err := w.Write(record); err != nil {
				return "", err
			}
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// FormatMarkdownSummary generates a Markdown Impact Analysis & Data Table report
// suitable for posting directly into a Renovate PR comment or GitHub Step Summary.
func (r *RunReport) FormatMarkdownSummary() string {
	impact := r.ComputeImpactSummary()
	var b strings.Builder

	fmt.Fprintf(&b, "## 🔍 `code-graft` Impact Analysis: `%s`\n\n", r.RecipeName)
	fmt.Fprintf(&b, "| Metric | Count |\n| :--- | :--- |\n")
	fmt.Fprintf(&b, "| **Files Scanned** | %d |\n", r.FilesScanned)
	fmt.Fprintf(&b, "| **Files Modified** | %d |\n", r.FilesChanged)
	fmt.Fprintf(&b, "| **Affected Files (Code + Audit Findings)** | %d |\n", impact.AffectedFiles)
	fmt.Fprintf(&b, "| **Total Data Table Findings** | %d |\n", impact.TotalFindings)
	if r.Verification != nil && r.Verification.Attempted {
		status := "✅ Passed"
		if !r.Verification.Passed {
			if r.Verification.RolledBack {
				status = "❌ Failed (Changes Rolled Back)"
			} else {
				status = "❌ Failed"
			}
		}
		fmt.Fprintf(&b, "| **Build & Test Verification** | %s |\n", status)
	}
	b.WriteString("\n")

	if r.Verification != nil && len(r.Verification.Diagnostics) > 0 {
		b.WriteString("### ⚠️ Verification Compiler Diagnostics\n\n")
		b.WriteString("| File | Line | Col | Message |\n| :--- | :--- | :--- | :--- |\n")
		for _, d := range r.Verification.Diagnostics {
			fmt.Fprintf(&b, "| `%s` | %d | %d | `%s` |\n",
				d.File, d.Line, d.Column, strings.ReplaceAll(d.Message, "|", "\\|"))
		}
		b.WriteString("\n")
	}

	if len(r.DataTables) > 0 {
		tableNames := make([]string, 0, len(r.DataTables))
		for tName := range r.DataTables {
			tableNames = append(tableNames, tName)
		}
		sort.Strings(tableNames)

		for _, tName := range tableNames {
			rows := r.DataTables[tName]
			if len(rows) == 0 {
				continue
			}
			fmt.Fprintf(&b, "### 📊 Data Table: `%s` (%d rows)\n\n", tName, len(rows))

			// Collect custom columns for this table
			colSet := make(map[string]bool)
			for _, row := range rows {
				for k := range row.Values {
					colSet[k] = true
				}
			}
			cols := make([]string, 0, len(colSet))
			for k := range colSet {
				cols = append(cols, k)
			}
			sort.Strings(cols)

			b.WriteString("| File | Line | Rule ID | Snippet |")
			for _, c := range cols {
				fmt.Fprintf(&b, " %s |", c)
			}
			b.WriteString("\n| :--- | :--- | :--- | :--- |")
			for range cols {
				b.WriteString(" :--- |")
			}
			b.WriteString("\n")

			for _, row := range rows {
				fmt.Fprintf(&b, "| `%s` | %d | `%s` | `%s` |",
					row.File, row.Line, row.RuleID, strings.ReplaceAll(row.Snippet, "|", "\\|"))
				for _, c := range cols {
					fmt.Fprintf(&b, " %s |", strings.ReplaceAll(row.Values[c], "|", "\\|"))
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// DiscoverRenovateRecipe scans catalogDir for YAML recipes whose `manifests` section
// matches a Renovate `{{{depName}}}@{{{newVersion}}}` (or `<depName>`) upgrade string,
// and returns a composed RecipeSpec containing all matching migration rules.
func DiscoverRenovateRecipe(catalogDir string, upgradeSpec string) (*RecipeSpec, error) {
	depName := strings.TrimSpace(upgradeSpec)
	newVer := ""
	if idx := strings.LastIndex(depName, "@"); idx > 0 {
		newVer = strings.TrimSpace(depName[idx+1:])
		depName = strings.TrimSpace(depName[:idx])
	}
	if depName == "" {
		return nil, fmt.Errorf("empty dependency name in %q", upgradeSpec)
	}

	var matchedSpecs []*RecipeSpec
	err := filepath.WalkDir(catalogDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		spec, loadErr := LoadRecipeYAML(path)
		if loadErr != nil {
			return nil
		}
		// Avoid matching composite bundle files if individual recipes are also in catalogDir
		if len(spec.Includes) > 0 {
			return nil
		}
		for _, m := range spec.Manifests {
			if m.OldPackage == depName || m.NewPackage == depName {
				matchedSpecs = append(matchedSpecs, spec)
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(matchedSpecs) == 0 {
		return nil, fmt.Errorf("no recipe found in %s matching dependency %q", catalogDir, depName)
	}

	composed := &RecipeSpec{
		Name:        fmt.Sprintf("renovate-auto-migration(%s)", upgradeSpec),
		Version:     newVer,
		Category:    "renovate-post-upgrade",
		Description: fmt.Sprintf("Auto-discovered migration recipe for Renovate upgrade of %s", upgradeSpec),
	}
	for _, s := range matchedSpecs {
		composed.Rules = append(composed.Rules, s.Rules...)
		for _, m := range s.Manifests {
			if newVer != "" && (m.OldPackage == depName || m.NewPackage == depName) {
				m.NewVersion = newVer
			}
			composed.Manifests = append(composed.Manifests, m)
		}
	}
	return composed, nil
}
