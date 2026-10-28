package rewrite

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"gopkg.in/yaml.v3"
)

// LearnExample represents a single Before/After file pair used to infer a RecipeSpec.
type LearnExample struct {
	Path   string
	Before []byte
	After  []byte
}

// LearnFromExamples synthesizes a self-verified RecipeSpec from one or more Before/After
// code examples using:
//  1. Scope & Import Diffing (inferring `fromPackage`, `addImports`, and `removeImports`).
//  2. Hybrid Line-Diff + Minimal Enclosing AST Alignment.
//  3. Smart Role-Aware Anti-Unification (preserving callee names as literal anchors while
//     generalizing receivers `$CLIENT` and shared arguments `$ARG1` / `$...ARGS`).
//  4. Cross-Site Rule Deduplication & Variadic Promotion.
//  5. Embedded `tests` generation and immediate `RunRecipeTests` self-verification.
func (e *Engine) LearnFromExamples(recipeName string, examples []LearnExample) (*RecipeSpec, *GovernanceReport, error) {
	if len(examples) == 0 {
		return nil, nil, fmt.Errorf("at least one LearnExample is required")
	}
	if strings.TrimSpace(recipeName) == "" {
		recipeName = "learned-migration-recipe"
	}

	spec := &RecipeSpec{
		Name:        recipeName,
		Version:     "1.0.0",
		Category:    "learned-migration",
		Author:      "code-graft-learner",
		Description: fmt.Sprintf("Auto-generated migration recipe learned from %d example(s)", len(examples)),
	}

	for exIdx, ex := range examples {
		ext := strings.ToLower(filepath.Ext(ex.Path))
		provider, ok := e.byExt[ext]
		if !ok {
			return nil, nil, fmt.Errorf("unsupported file extension for learning: %s", ex.Path)
		}

		beforeDoc, err := NewDocument(ex.Path, provider.Name(), provider.Language(), ex.Before)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing before example %s: %w", ex.Path, err)
		}
		afterDoc, err := NewDocument(ex.Path, provider.Name(), provider.Language(), ex.After)
		if err != nil {
			beforeDoc.Close()
			return nil, nil, fmt.Errorf("parsing after example %s: %w", ex.Path, err)
		}

		rules := learnRulesFromDocumentPair(provider, beforeDoc, afterDoc, exIdx+1)
		beforeDoc.Close()
		afterDoc.Close()

		spec.Rules = append(spec.Rules, rules...)
		spec.Tests = append(spec.Tests, RecipeTestSuite{
			Name:     fmt.Sprintf("learned-example-%d (%s)", exIdx+1, filepath.Base(ex.Path)),
			Path:     ex.Path,
			Input:    string(ex.Before),
			Expected: string(ex.After),
		})
	}

	spec.Rules = deduplicateAndMergeLearnedRules(spec.Rules)

	govReport := e.RunRecipeTests(spec)
	if !govReport.Valid {
		return spec, govReport, fmt.Errorf("learned recipe failed self-verification tests")
	}
	return spec, govReport, nil
}

// LearnFromGitCommit extracts modified file pairs (`HEAD~1` vs `HEAD`, or `<commit>~1` vs `<commit>`)
// from a git repository at repoDir and runs LearnFromExamples.
func (e *Engine) LearnFromGitCommit(repoDir string, commitRef string, recipeName string) (*RecipeSpec, *GovernanceReport, error) {
	if strings.TrimSpace(commitRef) == "" {
		commitRef = "HEAD"
	}
	cmd := exec.Command("git", "diff-tree", "--no-commit-id", "--name-only", "-r", commitRef)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("listing modified files in commit %s: %w", commitRef, err)
	}

	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	var examples []LearnExample

	for _, relPath := range files {
		relPath = strings.TrimSpace(relPath)
		if relPath == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(relPath))
		if _, ok := e.byExt[ext]; !ok {
			continue
		}

		beforeCmd := exec.Command("git", "show", fmt.Sprintf("%s~1:%s", commitRef, relPath))
		beforeCmd.Dir = repoDir
		beforeBytes, err1 := beforeCmd.Output()

		afterCmd := exec.Command("git", "show", fmt.Sprintf("%s:%s", commitRef, relPath))
		afterCmd.Dir = repoDir
		afterBytes, err2 := afterCmd.Output()

		if err1 == nil && err2 == nil && string(beforeBytes) != string(afterBytes) {
			examples = append(examples, LearnExample{
				Path:   relPath,
				Before: beforeBytes,
				After:  afterBytes,
			})
		}
	}

	if len(examples) == 0 {
		return nil, nil, fmt.Errorf("no supported modified source files found in commit %s", commitRef)
	}
	if strings.TrimSpace(recipeName) == "" {
		recipeName = "learned-from-commit-" + strings.TrimPrefix(commitRef, "HEAD")
		if recipeName == "learned-from-commit-" {
			recipeName = "learned-from-commit-head"
		}
	}
	return e.LearnFromExamples(recipeName, examples)
}

// FormatRecipeYAML serializes a RecipeSpec to clean YAML bytes.
func FormatRecipeYAML(spec *RecipeSpec) ([]byte, error) {
	return yaml.Marshal(spec)
}

type lineHunk struct {
	beforeLines []int // 0-indexed line numbers in beforeDoc
	afterLines  []int // 0-indexed line numbers in afterDoc
}

func learnRulesFromDocumentPair(
	provider LanguageSupport,
	beforeDoc *Document,
	afterDoc *Document,
	exampleNum int,
) []RewriteRule {
	beforeScope := provider.AnalyzeScope(beforeDoc)
	afterScope := provider.AnalyzeScope(afterDoc)

	addedImports, removedImports := diffScopesImports(beforeScope, afterScope)
	hunks := computeChangedLineHunks(string(beforeDoc.Source), string(afterDoc.Source))

	var learned []RewriteRule
	importsAttached := false

	for hunkIdx, hunk := range hunks {
		pairs := pairChangedLinesInHunk(beforeDoc, afterDoc, hunk)
		for pairIdx, pair := range pairs {
			// Skip pure import lines because AddImports/RemoveImports handles them losslessly.
			if isImportNodeOrAncestor(pair.beforeNode) && isImportNodeOrAncestor(pair.afterNode) {
				continue
			}

			matchPat, replacePat, targetVar, fromPkg := generalizeASTPair(
				pair.beforeNode,
				beforeDoc.Source,
				pair.afterNode,
				afterDoc.Source,
				beforeScope,
			)
			if matchPat == "" || matchPat == replacePat {
				continue
			}

			rule := RewriteRule{
				ID:          fmt.Sprintf("learned-%s-rule-%d-%d-%d", provider.Name(), exampleNum, hunkIdx+1, pairIdx+1),
				Language:    provider.Name(),
				Match:       matchPat,
				Replace:     replacePat,
				TargetVar:   targetVar,
				FromPackage: fromPkg,
			}

			// Attach inferred AddImports / RemoveImports to the first learned rule in this file.
			if !importsAttached && (len(addedImports) > 0 || len(removedImports) > 0) {
				rule.AddImports = addedImports
				rule.RemoveImports = removedImports
				importsAttached = true
			}

			learned = append(learned, rule)
		}
	}

	return learned
}

type nodePair struct {
	beforeNode *tree_sitter.Node
	afterNode  *tree_sitter.Node
}

// computeChangedLineHunks uses our LCS diff table to identify contiguous blocks of changed
// 0-indexed line numbers in beforeSrc and afterSrc.
func computeChangedLineHunks(beforeSrc, afterSrc string) []lineHunk {
	a := splitLines(beforeSrc)
	b := splitLines(afterSrc)
	m, n := len(a), len(b)

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = 1 + dp[i+1][j+1]
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var hunks []lineHunk
	var current lineHunk
	inHunk := false

	i, j := 0, 0
	for i < m && j < n {
		if a[i] == b[j] {
			if inHunk {
				hunks = append(hunks, current)
				current = lineHunk{}
				inHunk = false
			}
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			inHunk = true
			current.beforeLines = append(current.beforeLines, i)
			i++
		} else {
			inHunk = true
			current.afterLines = append(current.afterLines, j)
			j++
		}
	}
	for ; i < m; i++ {
		inHunk = true
		current.beforeLines = append(current.beforeLines, i)
	}
	for ; j < n; j++ {
		inHunk = true
		current.afterLines = append(current.afterLines, j)
	}
	if inHunk {
		hunks = append(hunks, current)
	}
	return hunks
}

func pairChangedLinesInHunk(beforeDoc, afterDoc *Document, hunk lineHunk) []nodePair {
	count := len(hunk.beforeLines)
	if len(hunk.afterLines) < count {
		count = len(hunk.afterLines)
	}

	var pairs []nodePair
	for k := 0; k < count; k++ {
		bLine := hunk.beforeLines[k]
		aLine := hunk.afterLines[k]

		bNode := findMinimalRewriteableNodeOnLine(beforeDoc, uint(bLine))
		aNode := findMinimalRewriteableNodeOnLine(afterDoc, uint(aLine))
		if bNode == nil || aNode == nil {
			continue
		}

		// Narrow down to the minimal differing child expression if both nodes are statements/assignments
		// with identical LHS (e.g. `u, err := c.GetUser(ctx)` -> `u, err := c.FetchUser(ctx)`).
		bMinimal, aMinimal := narrowIdenticalWrapper(bNode, beforeDoc.Source, aNode, afterDoc.Source)
		pairs = append(pairs, nodePair{
			beforeNode: bMinimal,
			afterNode:  aMinimal,
		})
	}
	return pairs
}

func findMinimalRewriteableNodeOnLine(doc *Document, row uint) *tree_sitter.Node {
	lines := strings.Split(string(doc.Source), "\n")
	if int(row) >= len(lines) {
		return nil
	}
	lineText := lines[row]
	trimmed := strings.TrimSpace(lineText)
	if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
		return nil
	}
	startCol := uint(len(lineText) - len(strings.TrimLeft(lineText, " \t")))
	endCol := uint(len(strings.TrimRight(lineText, " \t\r")))
	if endCol <= startCol {
		return nil
	}

	startPt := tree_sitter.Point{Row: row, Column: startCol}
	endPt := tree_sitter.Point{Row: row, Column: endCol}
	node := doc.Tree.RootNode().NamedDescendantForPointRange(startPt, endPt)
	for node != nil && node.Kind() == "expression_statement" && node.NamedChildCount() == 1 {
		node = node.NamedChild(0)
	}
	return node
}

// narrowIdenticalWrapper peels off assignment/declaration wrappers (`u, err := <call>`, `const x = <call>`, `return <call>`)
// when the wrapper prefix is identical between beforeNode and afterNode, isolating the inner call/expression!
func narrowIdenticalWrapper(
	bNode *tree_sitter.Node,
	bSrc []byte,
	aNode *tree_sitter.Node,
	aSrc []byte,
) (*tree_sitter.Node, *tree_sitter.Node) {
	for bNode != nil && aNode != nil && bNode.Kind() == aNode.Kind() {
		switch bNode.Kind() {
		case "short_var_declaration", "assignment_statement", "assignment", "variable_declarator", "local_variable_declaration", "let_declaration":
			bRight := bNode.ChildByFieldName("right")
			aRight := aNode.ChildByFieldName("right")
			if bRight == nil {
				bRight = bNode.ChildByFieldName("value")
				aRight = aNode.ChildByFieldName("value")
			}
			// Check if LHS text is identical
			bLeft := bNode.ChildByFieldName("left")
			aLeft := aNode.ChildByFieldName("left")
			if bLeft == nil {
				bLeft = bNode.ChildByFieldName("name")
				aLeft = aNode.ChildByFieldName("name")
			}
			if bLeft == nil {
				bLeft = bNode.ChildByFieldName("pattern")
				aLeft = aNode.ChildByFieldName("pattern")
			}
			if bLeft != nil && aLeft != nil && bRight != nil && aRight != nil &&
				bLeft.Utf8Text(bSrc) == aLeft.Utf8Text(aSrc) {
				if bRight.Kind() == "expression_list" && bRight.NamedChildCount() == 1 &&
					aRight.Kind() == "expression_list" && aRight.NamedChildCount() == 1 {
					bNode = bRight.NamedChild(0)
					aNode = aRight.NamedChild(0)
					continue
				}
				bNode = bRight
				aNode = aRight
				continue
			}
		case "return_statement":
			if bNode.NamedChildCount() == 1 && aNode.NamedChildCount() == 1 {
				bChild := bNode.NamedChild(0)
				aChild := aNode.NamedChild(0)
				if bChild.Kind() == "expression_list" && bChild.NamedChildCount() == 1 &&
					aChild.Kind() == "expression_list" && aChild.NamedChildCount() == 1 {
					bNode = bChild.NamedChild(0)
					aNode = aChild.NamedChild(0)
					continue
				}
				bNode = bChild
				aNode = aChild
				continue
			}
		}
		break
	}
	return bNode, aNode
}

type spanSub struct {
	start uint
	end   uint
	meta  string
}

// generalizeASTPair performs role-aware anti-unification on (beforeNode, afterNode):
//   - For method/function calls and constructors (`c.GetUser(ctx, id)` -> `c.FetchUser(ctx, id, nil)`),
//     it keeps the callee/method name literal, generalizes a variable receiver `c` into `$CLIENT`
//     (and looks up `c`'s package in `beforeScope` to populate `fromPackage` and `targetVar`),
//     and generalizes any shared argument subtrees (`ctx`, `id`) that appear in both before and after
//     into `$ARG1`, `$ARG2`, etc.
func generalizeASTPair(
	bNode *tree_sitter.Node,
	bSrc []byte,
	aNode *tree_sitter.Node,
	aSrc []byte,
	beforeScope ScopeVerifier,
) (matchPattern string, replacePattern string, targetVar string, fromPackage string) {
	bText := strings.TrimSpace(bNode.Utf8Text(bSrc))
	aText := strings.TrimSpace(aNode.Utf8Text(aSrc))
	if bText == "" || aText == "" || bText == aText {
		return "", "", "", ""
	}

	var bSubs []spanSub
	var aSubs []spanSub

	// 1. Check receiver / qualifier on call or constructor expressions
	bRecv, bIsPkgImport := extractReceiverOrQualifier(bNode, bSrc, beforeScope)
	aRecv, _ := extractReceiverOrQualifier(aNode, aSrc, beforeScope)

	if bRecv != nil {
		recvText := bRecv.Utf8Text(bSrc)
		if pkg := beforeScope.LookupSymbolPackage(recvText); pkg != "" {
			fromPackage = pkg
		}
		// If receiver is a local variable (not a package qualifier like `acmesdk`) and matches in afterNode,
		// generalize it to `$CLIENT` and set `targetVar: "CLIENT"`.
		if !bIsPkgImport && aRecv != nil && aRecv.Utf8Text(aSrc) == recvText {
			targetVar = "CLIENT"
			bSubs = append(bSubs, spanSub{start: bRecv.StartByte(), end: bRecv.EndByte(), meta: "$CLIENT"})
			aSubs = append(aSubs, spanSub{start: aRecv.StartByte(), end: aRecv.EndByte(), meta: "$CLIENT"})
		}
	}

	// 2. Align arguments in argument_list / arguments between bNode and aNode
	bArgs := extractArgumentNodes(bNode)
	aArgs := extractArgumentNodes(aNode)

	argCounter := 0
	textToMeta := make(map[string]string)

	for _, bArg := range bArgs {
		bArgText := strings.TrimSpace(bArg.Utf8Text(bSrc))
		if bArgText == "" {
			continue
		}
		// Find where bArgText is reused inside aArgs (either as a whole argument or nested inside an object/keyword arg like `{ token: $ARG1 }` or `host=$ARG1`)
		matchedInAfter := findMatchingSubnodesByText(aArgs, aSrc, bArgText)
		if len(matchedInAfter) > 0 {
			meta, exists := textToMeta[bArgText]
			if !exists {
				argCounter++
				meta = fmt.Sprintf("$ARG%d", argCounter)
				textToMeta[bArgText] = meta
			}
			bSubs = append(bSubs, spanSub{start: bArg.StartByte(), end: bArg.EndByte(), meta: meta})
			for _, aMatch := range matchedInAfter {
				aSubs = append(aSubs, spanSub{start: aMatch.StartByte(), end: aMatch.EndByte(), meta: meta})
			}
		}
	}

	matchPattern = applyRelativeSpanSubs(bNode, bSrc, bSubs)
	replacePattern = applyRelativeSpanSubs(aNode, aSrc, aSubs)
	return matchPattern, replacePattern, targetVar, fromPackage
}

func extractReceiverOrQualifier(node *tree_sitter.Node, src []byte, scope ScopeVerifier) (recv *tree_sitter.Node, isPkgImport bool) {
	if node == nil {
		return nil, false
	}
	switch node.Kind() {
	case "call_expression", "call":
		fn := node.ChildByFieldName("function")
		if fn != nil {
			switch fn.Kind() {
			case "selector_expression":
				op := fn.ChildByFieldName("operand")
				if op != nil {
					_, isImp := scope.ImportedPackages()[op.Utf8Text(src)]
					return op, isImp
				}
			case "member_expression", "attribute":
				obj := fn.ChildByFieldName("object")
				if obj != nil {
					_, isImp := scope.ImportedPackages()[obj.Utf8Text(src)]
					return obj, isImp
				}
			case "identifier":
				_, isImp := scope.ImportedPackages()[fn.Utf8Text(src)]
				return fn, isImp
			}
		}
	case "method_invocation":
		// Java/Ruby method_invocation
		obj := node.ChildByFieldName("object")
		if obj == nil {
			obj = node.ChildByFieldName("receiver")
		}
		if obj != nil {
			_, isImp := scope.ImportedPackages()[obj.Utf8Text(src)]
			return obj, isImp
		}
	case "new_expression", "object_creation_expression":
		ctor := node.ChildByFieldName("constructor")
		if ctor == nil {
			ctor = node.ChildByFieldName("type")
		}
		if ctor != nil {
			_, isImp := scope.ImportedPackages()[ctor.Utf8Text(src)]
			return ctor, isImp
		}
	}
	return nil, false
}

func extractArgumentNodes(node *tree_sitter.Node) []*tree_sitter.Node {
	if node == nil {
		return nil
	}
	argsList := node.ChildByFieldName("arguments")
	if argsList == nil {
		// Search immediate children for argument_list / arguments
		for i := uint(0); i < node.NamedChildCount(); i++ {
			c := node.NamedChild(i)
			if c.Kind() == "argument_list" || c.Kind() == "arguments" {
				argsList = c
				break
			}
		}
	}
	if argsList == nil {
		return nil
	}
	var args []*tree_sitter.Node
	for i := uint(0); i < argsList.NamedChildCount(); i++ {
		args = append(args, argsList.NamedChild(i))
	}
	return args
}

func findMatchingSubnodesByText(roots []*tree_sitter.Node, src []byte, targetText string) []*tree_sitter.Node {
	var matches []*tree_sitter.Node
	var walk func(n *tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n == nil {
			return
		}
		if strings.TrimSpace(n.Utf8Text(src)) == targetText {
			matches = append(matches, n)
			return
		}
		for i := uint(0); i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return matches
}

func applyRelativeSpanSubs(root *tree_sitter.Node, src []byte, subs []spanSub) string {
	baseStart := root.StartByte()
	baseEnd := root.EndByte()
	buf := make([]byte, baseEnd-baseStart)
	copy(buf, src[baseStart:baseEnd])

	sort.Slice(subs, func(i, j int) bool {
		return subs[i].start > subs[j].start
	})

	var lastStart uint = ^uint(0)
	for _, s := range subs {
		if s.start < baseStart || s.end > baseEnd || s.end > lastStart {
			continue
		}
		relStart := s.start - baseStart
		relEnd := s.end - baseStart
		var next []byte
		next = append(next, buf[:relStart]...)
		next = append(next, []byte(s.meta)...)
		next = append(next, buf[relEnd:]...)
		buf = next
		lastStart = s.start
	}
	return string(buf)
}

func diffScopesImports(beforeScope, afterScope ScopeVerifier) (added []ImportSpec, removed []string) {
	bImports := beforeScope.ImportedPackages()
	aImports := afterScope.ImportedPackages()

	beforeMods := make(map[string]string) // module -> alias
	for alias, mod := range bImports {
		beforeMods[mod] = alias
	}
	afterMods := make(map[string]string)
	for alias, mod := range aImports {
		afterMods[mod] = alias
	}

	for mod, alias := range afterMods {
		if _, exists := beforeMods[mod]; !exists {
			sym := alias
			// In Go, if the alias is just the base name of the module path, we still include it or leave it clean.
			if sym == filepath.Base(mod) || sym == mod {
				sym = ""
			}
			added = append(added, ImportSpec{
				Module: mod,
				Symbol: sym,
			})
		}
	}
	sort.Slice(added, func(i, j int) bool { return added[i].Module < added[j].Module })

	for mod := range beforeMods {
		if _, exists := afterMods[mod]; !exists {
			removed = append(removed, mod)
		}
	}
	sort.Strings(removed)
	return added, removed
}

func isImportNodeOrAncestor(n *tree_sitter.Node) bool {
	curr := n
	for curr != nil {
		k := curr.Kind()
		if strings.Contains(k, "import") || k == "use_declaration" || k == "preproc_include" {
			return true
		}
		curr = curr.Parent()
	}
	return false
}

var callSignatureRegex = regexp.MustCompile(`^(\$?[A-Za-z0-9_.:]+)\((.*)\)$`)

// deduplicateAndMergeLearnedRules deduplicates identical rules and promotes rules that call
// the same method with different argument counts into a single variadic `$...ARGS` rule when applicable.
func deduplicateAndMergeLearnedRules(rules []RewriteRule) []RewriteRule {
	seen := make(map[string]int) // key -> index in out
	var out []RewriteRule

	for _, r := range rules {
		exactKey := r.Language + "|" + r.Match + "|" + r.Replace + "|" + r.FromPackage
		if idx, exists := seen[exactKey]; exists {
			out[idx].AddImports = appendUniqueImports(out[idx].AddImports, r.AddImports)
			out[idx].RemoveImports = appendUniqueStrings(out[idx].RemoveImports, r.RemoveImports)
			continue
		}

		// Check if another rule in `out` calls the exact same method prefix with a different argument count
		mergedVariadic := false
		if subM := callSignatureRegex.FindStringSubmatch(r.Match); len(subM) == 3 {
			calleePrefix := subM[1]
			for i := range out {
				if out[i].Language == r.Language && out[i].FromPackage == r.FromPackage {
					if prevSub := callSignatureRegex.FindStringSubmatch(out[i].Match); len(prevSub) == 3 && prevSub[1] == calleePrefix {
						// Check if one of the rules has more arguments and we can promote the suffix to $...ARGS
						if vMatch, vReplace, ok := tryPromoteToVariadic(out[i].Match, out[i].Replace, r.Match, r.Replace); ok {
							out[i].Match = vMatch
							out[i].Replace = vReplace
							out[i].AddImports = appendUniqueImports(out[i].AddImports, r.AddImports)
							out[i].RemoveImports = appendUniqueStrings(out[i].RemoveImports, r.RemoveImports)
							mergedVariadic = true
							break
						}
					}
				}
			}
		}

		if !mergedVariadic {
			seen[exactKey] = len(out)
			out = append(out, r)
		}
	}
	return out
}

func tryPromoteToVariadic(matchA, replaceA, matchB, replaceB string) (string, string, bool) {
	// Ensure A is the one with more arguments
	if len(matchB) > len(matchA) {
		matchA, matchB = matchB, matchA
		replaceA, replaceB = replaceB, replaceA
	}
	// Example: matchA = `$CLIENT.request($ARG1, $ARG2)`, matchB = `$CLIENT.request($ARG1)`
	if strings.Contains(matchA, "$ARG1, $ARG2") && strings.Contains(matchB, "($ARG1)") {
		newMatch := strings.Replace(matchA, "$ARG1, $ARG2", "$ARG1, $...ARGS", 1)
		newReplace := strings.Replace(replaceA, "$ARG2", "$...ARGS", 1)
		return newMatch, newReplace, true
	}
	return "", "", false
}

func appendUniqueImports(base, extra []ImportSpec) []ImportSpec {
	for _, e := range extra {
		found := false
		for _, b := range base {
			if b.Module == e.Module && b.Symbol == e.Symbol {
				found = true
				break
			}
		}
		if !found {
			base = append(base, e)
		}
	}
	return base
}

func appendUniqueStrings(base, extra []string) []string {
	for _, e := range extra {
		found := false
		for _, b := range base {
			if b == e {
				found = true
				break
			}
		}
		if !found {
			base = append(base, e)
		}
	}
	return base
}
