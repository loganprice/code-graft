package rewrite

import (
	"fmt"
	"regexp"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// CompiledRule represents a pre-compiled composite rule tree (including atomic patterns,
// regexes, metavariable filters, relational operators inside/has/follows/precedes,
// and logical combinators all/any/not).
type CompiledRule struct {
	pattern  *Pattern
	kind     string
	regex    *regexp.Regexp
	stopBy   string
	where    map[string]*regexp.Regexp
	inside   *CompiledRule
	has      *CompiledRule
	follows  *CompiledRule
	precedes *CompiledRule
	all      []*CompiledRule
	any      []*CompiledRule
	not      *CompiledRule
}

// CompileRuleConstraint compiles a declarative RuleConstraint tree into an executable CompiledRule.
func CompileRuleConstraint(
	c RuleConstraint,
	langName string,
	lang *tree_sitter.Language,
	wrappers []ExpressionWrapperFunc,
) (*CompiledRule, error) {
	cr := &CompiledRule{
		kind:   strings.TrimSpace(c.Kind),
		stopBy: strings.ToLower(strings.TrimSpace(c.StopBy)),
	}

	if strings.TrimSpace(c.Pattern) != "" {
		pat, err := CompilePattern(c.Pattern, langName, lang, wrappers)
		if err != nil {
			return nil, err
		}
		cr.pattern = pat
	}

	if c.Regex != "" {
		re, err := regexp.Compile(c.Regex)
		if err != nil {
			cr.Close()
			return nil, fmt.Errorf("invalid regex %q: %w", c.Regex, err)
		}
		cr.regex = re
	}

	if len(c.Where) > 0 {
		cr.where = make(map[string]*regexp.Regexp, len(c.Where))
		for k, expr := range c.Where {
			re, err := regexp.Compile(expr)
			if err != nil {
				cr.Close()
				return nil, fmt.Errorf("invalid where regex for $%s (%q): %w", k, expr, err)
			}
			cr.where[strings.TrimPrefix(k, "$")] = re
		}
	}

	var err error
	if c.Inside != nil {
		cr.inside, err = CompileRuleConstraint(*c.Inside, langName, lang, wrappers)
		if err != nil {
			cr.Close()
			return nil, err
		}
	}
	if c.Has != nil {
		cr.has, err = CompileRuleConstraint(*c.Has, langName, lang, wrappers)
		if err != nil {
			cr.Close()
			return nil, err
		}
	}
	if c.Follows != nil {
		cr.follows, err = CompileRuleConstraint(*c.Follows, langName, lang, wrappers)
		if err != nil {
			cr.Close()
			return nil, err
		}
	}
	if c.Precedes != nil {
		cr.precedes, err = CompileRuleConstraint(*c.Precedes, langName, lang, wrappers)
		if err != nil {
			cr.Close()
			return nil, err
		}
	}
	if c.Not != nil {
		cr.not, err = CompileRuleConstraint(*c.Not, langName, lang, wrappers)
		if err != nil {
			cr.Close()
			return nil, err
		}
	}

	for _, sub := range c.All {
		compiledSub, subErr := CompileRuleConstraint(sub, langName, lang, wrappers)
		if subErr != nil {
			cr.Close()
			return nil, subErr
		}
		cr.all = append(cr.all, compiledSub)
	}

	for _, sub := range c.Any {
		compiledSub, subErr := CompileRuleConstraint(sub, langName, lang, wrappers)
		if subErr != nil {
			cr.Close()
			return nil, subErr
		}
		cr.any = append(cr.any, compiledSub)
	}

	return cr, nil
}

// Close releases all underlying Tree-sitter pattern documents in the compiled rule tree.
func (cr *CompiledRule) Close() {
	if cr == nil {
		return
	}
	if cr.pattern != nil {
		cr.pattern.Close()
		cr.pattern = nil
	}
	cr.inside.Close()
	cr.has.Close()
	cr.follows.Close()
	cr.precedes.Close()
	cr.not.Close()
	for _, sub := range cr.all {
		sub.Close()
	}
	for _, sub := range cr.any {
		sub.Close()
	}
}

// FindMatches walks doc's CST and returns all nodes that satisfy the full composite CompiledRule.
func (cr *CompiledRule) FindMatches(doc *Document) []MatchResult {
	var matches []MatchResult
	var walk func(n *tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n == nil {
			return
		}
		captures := make(map[string]Capture)
		if cr.Evaluate(n, doc.Source, captures) {
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

// Evaluate tests whether node in targetSrc satisfies every atomic, relational, and logical
// constraint in cr. Any metavariable bindings are transactionally merged into captures
// (and rolled back if a branch fails).
func (cr *CompiledRule) Evaluate(node *tree_sitter.Node, targetSrc []byte, captures map[string]Capture) bool {
	if cr == nil || node == nil {
		return false
	}

	// Work on a transactional copy of captures so failed branches don't pollute outer captures.
	working := cloneCaptures(captures)

	// 1. Atomic Kind check
	if cr.kind != "" && node.Kind() != cr.kind {
		return false
	}

	// 2. Atomic Pattern check
	if cr.pattern != nil {
		if !cr.pattern.MatchNode(node, targetSrc, working) {
			return false
		}
	}

	// 3. Atomic Regex check on node text
	if cr.regex != nil {
		if !cr.regex.Match(targetSrc[node.StartByte():node.EndByte()]) {
			return false
		}
	}

	// 4.Logical ALL (conjunction): every sub-constraint must hold on `node`
	for _, sub := range cr.all {
		if !sub.Evaluate(node, targetSrc, working) {
			return false
		}
	}

	// 5. Logical ANY (disjunction): at least one sub-constraint must hold on `node`
	if len(cr.any) > 0 {
		matchedAny := false
		for _, sub := range cr.any {
			branchCaps := cloneCaptures(working)
			if sub.Evaluate(node, targetSrc, branchCaps) {
				working = branchCaps
				matchedAny = true
				break
			}
		}
		if !matchedAny {
			return false
		}
	}

	// 6. Relational INSIDE: an ancestor must satisfy cr.inside
	if cr.inside != nil {
		if !evalInside(node, cr.inside, targetSrc, working) {
			return false
		}
	}

	// 7. Relational HAS: a descendant must satisfy cr.has
	if cr.has != nil {
		if !evalHas(node, cr.has, targetSrc, working) {
			return false
		}
	}

	// 8. Relational FOLLOWS: a preceding named sibling must satisfy cr.follows
	if cr.follows != nil {
		if !evalFollows(node, cr.follows, targetSrc, working) {
			return false
		}
	}

	// 9. Relational PRECEDES: a subsequent named sibling must satisfy cr.precedes
	if cr.precedes != nil {
		if !evalPrecedes(node, cr.precedes, targetSrc, working) {
			return false
		}
	}

	// 10. Logical NOT (negation): cr.not must NOT hold on `node` (does not bind captures)
	if cr.not != nil {
		negCaps := cloneCaptures(working)
		if cr.not.Evaluate(node, targetSrc, negCaps) {
			return false
		}
	}

	// 11. Metavariable `where` regex filters
	for varName, re := range cr.where {
		capVal, exists := working[varName]
		if !exists || !re.MatchString(capVal.Text) {
			return false
		}
	}

	// Commit captures back to caller map
	for k, v := range working {
		captures[k] = v
	}
	return true
}

func evalInside(node *tree_sitter.Node, target *CompiledRule, src []byte, captures map[string]Capture) bool {
	neighborOnly := target.stopBy == "neighbor"
	curr := node.Parent()
	for curr != nil {
		branchCaps := cloneCaptures(captures)
		if target.Evaluate(curr, src, branchCaps) {
			for k, v := range branchCaps {
				captures[k] = v
			}
			return true
		}
		if neighborOnly {
			break
		}
		curr = curr.Parent()
	}
	return false
}

func evalHas(node *tree_sitter.Node, target *CompiledRule, src []byte, captures map[string]Capture) bool {
	neighborOnly := target.stopBy == "neighbor"
	var search func(curr *tree_sitter.Node) bool
	search = func(curr *tree_sitter.Node) bool {
		for i := uint(0); i < curr.ChildCount(); i++ {
			child := curr.Child(i)
			if child == nil {
				continue
			}
			branchCaps := cloneCaptures(captures)
			if target.Evaluate(child, src, branchCaps) {
				for k, v := range branchCaps {
					captures[k] = v
				}
				return true
			}
			if !neighborOnly && search(child) {
				return true
			}
		}
		return false
	}
	return search(node)
}

func evalFollows(node *tree_sitter.Node, target *CompiledRule, src []byte, captures map[string]Capture) bool {
	neighborOnly := target.stopBy == "neighbor"
	curr := prevEffectiveSibling(node)
	for curr != nil {
		branchCaps := cloneCaptures(captures)
		if matchSiblingOrExpressionChild(curr, target, src, branchCaps) {
			for k, v := range branchCaps {
				captures[k] = v
			}
			return true
		}
		if neighborOnly {
			break
		}
		curr = curr.PrevNamedSibling()
	}
	return false
}

func evalPrecedes(node *tree_sitter.Node, target *CompiledRule, src []byte, captures map[string]Capture) bool {
	neighborOnly := target.stopBy == "neighbor"
	curr := nextEffectiveSibling(node)
	for curr != nil {
		branchCaps := cloneCaptures(captures)
		if matchSiblingOrExpressionChild(curr, target, src, branchCaps) {
			for k, v := range branchCaps {
				captures[k] = v
			}
			return true
		}
		if neighborOnly {
			break
		}
		curr = curr.NextNamedSibling()
	}
	return false
}

// prevEffectiveSibling returns the previous named sibling. If `node` is an expression
// wrapped inside an `expression_statement` that has no siblings of its own, it steps up
// to the statement level so statement-level sibling ordering works naturally.
func prevEffectiveSibling(node *tree_sitter.Node) *tree_sitter.Node {
	if sib := node.PrevNamedSibling(); sib != nil {
		return sib
	}
	if p := node.Parent(); p != nil && p.Kind() == "expression_statement" {
		return p.PrevNamedSibling()
	}
	return nil
}

func nextEffectiveSibling(node *tree_sitter.Node) *tree_sitter.Node {
	if sib := node.NextNamedSibling(); sib != nil {
		return sib
	}
	if p := node.Parent(); p != nil && p.Kind() == "expression_statement" {
		return p.NextNamedSibling()
	}
	return nil
}

// matchSiblingOrExpressionChild checks if sibling directly satisfies target, or if sibling is an
// `expression_statement` whose inner expression satisfies target.
func matchSiblingOrExpressionChild(sib *tree_sitter.Node, target *CompiledRule, src []byte, captures map[string]Capture) bool {
	if target.Evaluate(sib, src, captures) {
		return true
	}
	if sib.Kind() == "expression_statement" && sib.NamedChildCount() == 1 {
		return target.Evaluate(sib.NamedChild(0), src, captures)
	}
	return false
}

func cloneCaptures(src map[string]Capture) map[string]Capture {
	out := make(map[string]Capture, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
