package rewrite

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

const (
	metaPrefix     = "__ts_meta_"
	variadicPrefix = "__ts_variadic_"
	metaSuffix     = "__"
)

var (
	variadicPatternRegex = regexp.MustCompile(`\$\.\.\.([A-Za-z_][A-Za-z0-9_]*)`)
	singleMetaRegex      = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
)

// Capture holds the matched AST node and its byte range/text for a metavariable.
type Capture struct {
	Name      string
	Text      string
	StartByte uint
	EndByte   uint
}

// MatchResult represents a single CST subtree match against a compiled Pattern.
type MatchResult struct {
	Node     *tree_sitter.Node
	Captures map[string]Capture
}

// ExpressionWrapperFunc wraps a code snippet in a valid top-level construct for a grammar
// when the snippet alone cannot be parsed without syntax errors at the file root.
type ExpressionWrapperFunc func(snippet string) (wrapped string, unwrap func(root *tree_sitter.Node) *tree_sitter.Node)

// Pattern represents a compiled code-snippet pattern for a target language.
type Pattern struct {
	RawMatch    string
	LangName    string
	Language    *tree_sitter.Language
	templateSrc []byte
	templateDoc *Document
	patternRoot *tree_sitter.Node
}

// CompilePattern compiles a human-readable code snippet containing `$META` or `$...ARGS`
// metavariables into a structural CST matcher for the given language.
func CompilePattern(
	rawMatch string,
	langName string,
	lang *tree_sitter.Language,
	wrappers []ExpressionWrapperFunc,
) (*Pattern, error) {
	trimmed := strings.TrimSpace(rawMatch)
	if trimmed == "" {
		return nil, fmt.Errorf("pattern cannot be empty")
	}

	preprocessed := preprocessMetavars(trimmed)

	// 1. Try parsing directly at root level if no explicit wrappers are provided,
	// or if the root parse covers the entire trimmed snippet without errors.
	if len(wrappers) == 0 {
		doc, err := NewDocument("pattern", langName, lang, []byte(preprocessed))
		if err != nil {
			return nil, err
		}

		root := doc.Tree.RootNode()
		if !root.HasError() && root.NamedChildCount() > 0 {
			unwrapped := unwrapSingleStatementRoot(root)
			return &Pattern{
				RawMatch:    rawMatch,
				LangName:    langName,
				Language:    lang,
				templateSrc: doc.Source,
				templateDoc: doc,
				patternRoot: unwrapped,
			}, nil
		}
		doc.Close()
	}

	// 2. Try language-specific expression wrappers.
	for _, wrapFn := range wrappers {
		wrappedSrc, unwrapFn := wrapFn(preprocessed)
		wDoc, wErr := NewDocument("pattern_wrapped", langName, lang, []byte(wrappedSrc))
		if wErr != nil {
			continue
		}
		if !wDoc.Tree.RootNode().HasError() {
			extracted := unwrapFn(wDoc.Tree.RootNode())
			if extracted != nil {
				return &Pattern{
					RawMatch:    rawMatch,
					LangName:    langName,
					Language:    lang,
					templateSrc: wDoc.Source,
					templateDoc: wDoc,
					patternRoot: unwrapSingleStatementRoot(extracted),
				}, nil
			}
		}
		wDoc.Close()
	}

	// 3. Fallback: try parsing directly at root level (e.g. if snippet is a top-level func/import decl).
	doc, err := NewDocument("pattern", langName, lang, []byte(preprocessed))
	if err != nil {
		return nil, err
	}
	root := doc.Tree.RootNode()
	if !root.HasError() && root.NamedChildCount() > 0 {
		return &Pattern{
			RawMatch:    rawMatch,
			LangName:    langName,
			Language:    lang,
			templateSrc: doc.Source,
			templateDoc: doc,
			patternRoot: unwrapSingleStatementRoot(root),
		}, nil
	}
	doc.Close()

	return nil, fmt.Errorf("failed to parse pattern %q in language %s without syntax errors", rawMatch, langName)
}

// Close frees the underlying Tree-sitter template document.
func (p *Pattern) Close() {
	if p.templateDoc != nil {
		p.templateDoc.Close()
		p.templateDoc = nil
	}
}

// DebugSexp returns the S-expression of the compiled pattern root node.
func (p *Pattern) DebugSexp() string {
	if p.patternRoot == nil {
		return "<nil>"
	}
	return p.patternRoot.Kind() + ": " + p.patternRoot.ToSexp() + " (" + p.patternRoot.Utf8Text(p.templateSrc) + ")"
}

// FindMatches walks the target Document CST and returns all subtrees that unify with the Pattern.
func (p *Pattern) FindMatches(doc *Document) []MatchResult {
	var matches []MatchResult
	var walk func(n *tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n == nil {
			return
		}
		captures := make(map[string]Capture)
		if p.MatchNode(n, doc.Source, captures) {
			matches = append(matches, MatchResult{
				Node:     n,
				Captures: captures,
			})
		}
		for i := uint(0); i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	walk(doc.Tree.RootNode())
	return matches
}

// MatchNode checks whether candidateNode in targetSrc unifies with the Pattern,
// merging any new metavariable bindings into captures (and verifying consistency
// with any existing bindings already in captures).
func (p *Pattern) MatchNode(candidateNode *tree_sitter.Node, targetSrc []byte, captures map[string]Capture) bool {
	return p.unifyNode(p.patternRoot, candidateNode, targetSrc, captures)
}

// RenderReplacementWithTransforms applies optional MetavarTransforms to captured values
// and expands `$META`, `$...ARGS`, and `$0` (the entire matched node text) in replacementTemplate.
func RenderReplacementWithTransforms(
	replacementTemplate string,
	matchedText string,
	captures map[string]Capture,
	transforms map[string]MetavarTransform,
) ([]byte, error) {
	effective := make(map[string]Capture, len(captures)+1)
	for k, v := range captures {
		effective[k] = v
	}
	effective["0"] = Capture{Name: "0", Text: matchedText}

	for varName, tr := range transforms {
		cleanName := strings.TrimPrefix(varName, "$")
		if capVal, ok := effective[cleanName]; ok && tr.Regex != "" {
			re, err := regexp.Compile(tr.Regex)
			if err != nil {
				return nil, fmt.Errorf("invalid transform regex %q: %w", tr.Regex, err)
			}
			capVal.Text = re.ReplaceAllString(capVal.Text, tr.Replace)
			effective[cleanName] = capVal
		}
	}

	out := string(RenderReplacement(replacementTemplate, effective))
	out = strings.ReplaceAll(out, "$0", effective["0"].Text)
	return []byte(out), nil
}
func RenderReplacement(replacementTemplate string, captures map[string]Capture) []byte {
	// First expand variadic `$...NAME`
	out := variadicPatternRegex.ReplaceAllStringFunc(replacementTemplate, func(m string) string {
		sub := variadicPatternRegex.FindStringSubmatch(m)
		if len(sub) == 2 {
			if capVal, ok := captures[sub[1]]; ok {
				return capVal.Text
			}
		}
		return m
	})

	// Next expand single `$NAME`
	out = singleMetaRegex.ReplaceAllStringFunc(out, func(m string) string {
		sub := singleMetaRegex.FindStringSubmatch(m)
		if len(sub) == 2 {
			if capVal, ok := captures[sub[1]]; ok {
				return capVal.Text
			}
		}
		return m
	})

	// Clean up trailing/leading comma artifacts when variadic capture matched 0 arguments,
	// e.g., `foo(a, )` -> `foo(a)` or `foo(, a)` -> `foo(a)`.
	out = cleanEmptyVariadicCommas(out)
	return []byte(out)
}

func cleanEmptyVariadicCommas(s string) string {
	// Replace `(, ` with `(` and `, )` with `)`
	reLeading := regexp.MustCompile(`\(\s*,\s*`)
	s = reLeading.ReplaceAllString(s, "(")
	reTrailing := regexp.MustCompile(`,\s*\)`)
	s = reTrailing.ReplaceAllString(s, ")")
	return s
}

func preprocessMetavars(snippet string) string {
	s := variadicPatternRegex.ReplaceAllStringFunc(snippet, func(m string) string {
		sub := variadicPatternRegex.FindStringSubmatch(m)
		if len(sub) == 2 {
			return variadicPrefix + sub[1] + metaSuffix
		}
		return m
	})
	s = singleMetaRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := singleMetaRegex.FindStringSubmatch(m)
		if len(sub) == 2 {
			return metaPrefix + sub[1] + metaSuffix
		}
		return m
	})
	return s
}

func parsePlaceholder(text string) (name string, isMeta bool, isVariadic bool) {
	if strings.HasPrefix(text, variadicPrefix) && strings.HasSuffix(text, metaSuffix) {
		name = strings.TrimSuffix(strings.TrimPrefix(text, variadicPrefix), metaSuffix)
		return name, true, true
	}
	if strings.HasPrefix(text, metaPrefix) && strings.HasSuffix(text, metaSuffix) {
		name = strings.TrimSuffix(strings.TrimPrefix(text, metaPrefix), metaSuffix)
		return name, true, false
	}
	return "", false, false
}

// unwrapSingleStatementRoot peels off program/module/expression_statement wrappers
// so an expression pattern matches expression nodes directly inside larger statements.
func unwrapSingleStatementRoot(node *tree_sitter.Node) *tree_sitter.Node {
	curr := node
	for curr != nil {
		kind := curr.Kind()
		if isWrapperKind(kind) && curr.NamedChildCount() == 1 {
			curr = curr.NamedChild(0)
			continue
		}
		break
	}
	return curr
}

func isWrapperKind(kind string) bool {
	switch kind {
	case "source_file", "program", "module", "document", "block", "statement_block", "statement_list", "expression_statement":
		return true
	default:
		return false
	}
}

func isIdentifierLikeKind(kind string) bool {
	return strings.Contains(kind, "identifier") ||
		kind == "type_identifier" ||
		kind == "field_identifier" ||
		kind == "property_identifier" ||
		kind == "string_content" ||
		kind == "string" ||
		kind == "attribute_name" ||
		kind == "attribute_value" ||
		kind == "quoted_attribute_value" ||
		kind == "tag_name"
}

// unifyNode recursively checks whether candidateNode in targetSrc matches patternNode in p.templateSrc.
func (p *Pattern) unifyNode(
	patternNode *tree_sitter.Node,
	candidateNode *tree_sitter.Node,
	targetSrc []byte,
	captures map[string]Capture,
) bool {
	if patternNode == nil || candidateNode == nil {
		return patternNode == candidateNode
	}

	patText := strings.TrimSpace(patternNode.Utf8Text(p.templateSrc))
	if isIdentifierLikeKind(patternNode.Kind()) {
		unquotedPat := strings.Trim(patText, "\"'`")
		if name, isMeta, isVariadic := parsePlaceholder(unquotedPat); isMeta && !isVariadic {
			return bindCapture(captures, name, candidateNode.Utf8Text(targetSrc), candidateNode.StartByte(), candidateNode.EndByte())
		}
	}

	if patternNode.Kind() != candidateNode.Kind() {
		return false
	}

	patChildren := nonExtraChildren(patternNode)
	candChildren := nonExtraChildren(candidateNode)

	// If leaf node (no children), exact token text must match.
	if len(patChildren) == 0 && len(candChildren) == 0 {
		return bytes.Equal(
			bytes.TrimSpace(p.templateSrc[patternNode.StartByte():patternNode.EndByte()]),
			bytes.TrimSpace(targetSrc[candidateNode.StartByte():candidateNode.EndByte()]),
		)
	}

	return p.unifyChildren(patChildren, candChildren, targetSrc, captures)
}

func (p *Pattern) unifyChildren(
	patChildren []*tree_sitter.Node,
	candChildren []*tree_sitter.Node,
	targetSrc []byte,
	captures map[string]Capture,
) bool {
	// Check if patChildren contains a variadic placeholder node.
	for i, patChild := range patChildren {
		patText := strings.TrimSpace(patChild.Utf8Text(p.templateSrc))
		if name, isMeta, isVariadic := parsePlaceholder(patText); isMeta && isVariadic {
			prefixEnd := i
			// If the variadic placeholder is preceded by a comma and matches 0 elements,
			// that comma in the pattern won't exist in the candidate when 0 variadic args are passed.
			hasLeadingComma := prefixEnd > 0 && patChildren[prefixEnd-1].Kind() == ","
			minPrefix := prefixEnd
			if hasLeadingComma {
				minPrefix = prefixEnd - 1
			}

			suffixPat := patChildren[i+1:]
			if len(suffixPat) > 0 && suffixPat[0].Kind() == "," {
				suffixPat = suffixPat[1:]
			}

			if minPrefix+len(suffixPat) > len(candChildren) {
				return false
			}

			// Match prefix (excluding the optional leading comma before variadic)
			for k := 0; k < minPrefix; k++ {
				if !p.unifyNode(patChildren[k], candChildren[k], targetSrc, captures) {
					return false
				}
			}

			candSuffixStart := len(candChildren) - len(suffixPat)
			for k := 0; k < len(suffixPat); k++ {
				if !p.unifyNode(suffixPat[k], candChildren[candSuffixStart+k], targetSrc, captures) {
					return false
				}
			}

			// Everything in candChildren[minPrefix : candSuffixStart] belongs to the variadic capture.
			variadicSlice := candChildren[minPrefix:candSuffixStart]
			// Trim leading/trailing comma tokens from the variadic slice if present.
			for len(variadicSlice) > 0 && variadicSlice[0].Kind() == "," {
				variadicSlice = variadicSlice[1:]
			}
			for len(variadicSlice) > 0 && variadicSlice[len(variadicSlice)-1].Kind() == "," {
				variadicSlice = variadicSlice[:len(variadicSlice)-1]
			}

			if len(variadicSlice) == 0 {
				return bindCapture(captures, name, "", 0, 0)
			}
			startB := variadicSlice[0].StartByte()
			endB := variadicSlice[len(variadicSlice)-1].EndByte()
			return bindCapture(captures, name, string(targetSrc[startB:endB]), startB, endB)
		}
	}

	if len(patChildren) != len(candChildren) {
		return false
	}
	for i := range patChildren {
		if !p.unifyNode(patChildren[i], candChildren[i], targetSrc, captures) {
			return false
		}
	}
	return true
}

func bindCapture(captures map[string]Capture, name string, text string, startByte, endByte uint) bool {
	if existing, ok := captures[name]; ok {
		// Unification requires repeated metavariables (e.g. `$X == $X`) to have identical text.
		return existing.Text == text
	}
	captures[name] = Capture{
		Name:      name,
		Text:      text,
		StartByte: startByte,
		EndByte:   endByte,
	}
	return true
}

func nonExtraChildren(n *tree_sitter.Node) []*tree_sitter.Node {
	count := n.ChildCount()
	out := make([]*tree_sitter.Node, 0, count)
	for i := uint(0); i < count; i++ {
		c := n.Child(i)
		if c != nil && !c.IsExtra() {
			out = append(out, c)
		}
	}
	return out
}
