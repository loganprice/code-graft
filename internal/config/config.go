// Package config provides lossless structural rewriting for enterprise configuration
// and infrastructure files (YAML, .properties/.env, and Dockerfile) while preserving
// comments, indentation, and key ordering.
package config

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/loganprice/code-graft/pkg/rewrite"
	"gopkg.in/yaml.v3"
)

// ApplyConfigRules applies any matching `yaml`, `properties`, or `dockerfile` rules in recipe
// to the given file content, returning the updated content and total edits applied.
func ApplyConfigRules(path string, source []byte, rules []rewrite.RewriteRule) ([]byte, int, error) {
	langType := DetectConfigLanguage(path)
	if langType == "" {
		return source, 0, nil
	}

	current := source
	totalEdits := 0

	for _, rule := range rules {
		ruleLang := NormalizeConfigLang(rule.Language)
		if ruleLang != langType || !rule.Preconditions.MatchesFile(path) {
			continue
		}

		var updated []byte
		var count int
		var err error

		switch langType {
		case "yaml":
			updated, count, err = applyYAMLRule(current, rule)
		case "properties":
			updated, count, err = applyPropertiesRule(current, rule)
		case "dockerfile":
			updated, count, err = applyDockerfileRule(current, rule)
		}
		if err != nil {
			return current, totalEdits, fmt.Errorf("config rule %q: %w", rule.ID, err)
		}
		if count > 0 && !bytes.Equal(current, updated) {
			current = updated
			totalEdits += count
		}
	}

	return current, totalEdits, nil
}

// DetectConfigLanguage identifies whether path is a YAML, Properties, or Dockerfile config.
func DetectConfigLanguage(path string) string {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))

	if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") || ext == ".dockerfile" {
		return "dockerfile"
	}
	switch ext {
	case ".yaml", ".yml":
		return "yaml"
	case ".properties", ".env", ".ini", ".cfg":
		return "properties"
	default:
		return ""
	}
}

// NormalizeConfigLang normalizes language aliases for config/infrastructure rules.
func NormalizeConfigLang(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "yaml", "yml", "k8s", "kubernetes", "github-actions":
		return "yaml"
	case "properties", "props", "dotenv", "env", "ini":
		return "properties"
	case "dockerfile", "docker", "containerfile":
		return "dockerfile"
	default:
		return ""
	}
}

// applyYAMLRule uses yaml.v3's line/column-indexed AST to locate dotted key paths
// (e.g., `spring.redis.host: $VAL` or `image: $IMG`) and losslessly patch the original
// source buffer so comments and unrelated formatting remain 100% intact.
func applyYAMLRule(source []byte, rule rewrite.RewriteRule) ([]byte, int, error) {
	if rule.Regex != "" && strings.TrimSpace(rule.Match) == "" {
		return applyLineRegexRule(source, rule)
	}

	matchKeyPath, matchValExpr, ok := splitKeyValPattern(rule.Match)
	if !ok {
		return applyLineRegexRule(source, rule)
	}
	replKeyPath, replValExpr, replOk := splitKeyValPattern(rule.Replace)
	if !replOk {
		return applyLineRegexRule(source, rule)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(source, &root); err != nil {
		return source, 0, nil
	}

	matchSegments := strings.Split(matchKeyPath, ".")
	replSegments := strings.Split(replKeyPath, ".")
	lines := strings.Split(string(source), "\n")
	editsApplied := 0

	var walkYAML func(node *yaml.Node, pathIdx int)
	walkYAML = func(node *yaml.Node, pathIdx int) {
		if node == nil {
			return
		}
		if node.Kind == yaml.DocumentNode {
			for _, c := range node.Content {
				walkYAML(c, pathIdx)
			}
			return
		}
		if node.Kind == yaml.SequenceNode {
			for _, item := range node.Content {
				walkYAML(item, pathIdx)
			}
			return
		}
		if node.Kind != yaml.MappingNode {
			return
		}

		for i := 0; i+1 < len(node.Content); i += 2 {
			kNode := node.Content[i]
			vNode := node.Content[i+1]

			if kNode.Value == matchSegments[pathIdx] {
				if pathIdx == len(matchSegments)-1 {
					// Leaf key matched! Check value pattern/capture.
					captures := make(map[string]rewrite.Capture)
					if matchConfigValue(matchValExpr, vNode.Value, captures) && checkWhereConstraints(rule.Where, captures) {
						newLeafKey := replSegments[len(replSegments)-1]
						renderedVal, err := rewrite.RenderReplacementWithTransforms(
							replValExpr,
							vNode.Value,
							captures,
							rule.Transforms,
						)
						if err == nil {
							lineIdx := kNode.Line - 1
							if lineIdx >= 0 && lineIdx < len(lines) {
								updatedLine := replaceYAMLKeyValOnLine(lines[lineIdx], kNode.Value, newLeafKey, vNode.Value, string(renderedVal))
								if updatedLine != lines[lineIdx] {
									lines[lineIdx] = updatedLine
									editsApplied++
								}
							}
						}
					}
				} else {
					walkYAML(vNode, pathIdx+1)
				}
			}

			// Also allow matching unanchored single-segment keys (like `image: $IMG`) anywhere in the YAML tree.
			if len(matchSegments) == 1 && vNode.Kind == yaml.MappingNode || vNode.Kind == yaml.SequenceNode {
				walkYAML(vNode, 0)
			}
		}
	}

	walkYAML(&root, 0)
	if editsApplied == 0 {
		return source, 0, nil
	}
	return []byte(strings.Join(lines, "\n")), editsApplied, nil
}

func replaceYAMLKeyValOnLine(line, oldKey, newKey, oldVal, newVal string) string {
	// Preserve leading indentation and inline comments.
	comment := ""
	codePart := line
	if idx := strings.Index(line, " #"); idx >= 0 {
		codePart = line[:idx]
		comment = line[idx:]
	}
	indentLen := len(codePart) - len(strings.TrimLeft(codePart, " \t"))
	indent := codePart[:indentLen]
	return fmt.Sprintf("%s%s: %s%s", indent, newKey, newVal, comment)
}

// applyPropertiesRule rewrites `<key> = <val>` or `<key>: <val>` lines in `.properties` / `.env` files.
func applyPropertiesRule(source []byte, rule rewrite.RewriteRule) ([]byte, int, error) {
	matchKey, matchVal, ok := splitPropertiesPattern(rule.Match)
	if !ok {
		return applyLineRegexRule(source, rule)
	}
	replKey, replVal, replOk := splitPropertiesPattern(rule.Replace)
	if !replOk {
		return applyLineRegexRule(source, rule)
	}

	lines := strings.Split(string(source), "\n")
	editsApplied := 0
	propLineRegex := regexp.MustCompile(`^(\s*)([^#!\s=:]+)(\s*[=:]\s*)(.*?)\s*$`)

	for i, line := range lines {
		sub := propLineRegex.FindStringSubmatch(line)
		if len(sub) != 5 {
			continue
		}
		indent, key, sep, val := sub[1], sub[2], sub[3], sub[4]
		if key != matchKey {
			continue
		}
		captures := make(map[string]rewrite.Capture)
		if !matchConfigValue(matchVal, val, captures) || !checkWhereConstraints(rule.Where, captures) {
			continue
		}
		renderedVal, err := rewrite.RenderReplacementWithTransforms(replVal, val, captures, rule.Transforms)
		if err != nil {
			return source, 0, err
		}
		newLine := fmt.Sprintf("%s%s%s%s", indent, replKey, sep, string(renderedVal))
		if newLine != line {
			lines[i] = newLine
			editsApplied++
		}
	}

	if editsApplied == 0 {
		return source, 0, nil
	}
	return []byte(strings.Join(lines, "\n")), editsApplied, nil
}

// applyDockerfileRule rewrites Dockerfile instructions (`FROM $IMAGE:$TAG`, `ENV $K=$V`, etc.)
// using metavariable-aware instruction pattern matching.
func applyDockerfileRule(source []byte, rule rewrite.RewriteRule) ([]byte, int, error) {
	if strings.TrimSpace(rule.Match) == "" {
		return applyLineRegexRule(source, rule)
	}

	// Convert `$META` in rule.Match into named regex capture groups `(?P<META>[^\s:]+)` (or `.+` if at end of line)
	patternRegex, err := compileSnippetToLineRegex(rule.Match)
	if err != nil {
		return source, 0, err
	}

	lines := strings.Split(string(source), "\n")
	editsApplied := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		sub := patternRegex.FindStringSubmatch(trimmed)
		if sub == nil {
			continue
		}
		captures := make(map[string]rewrite.Capture)
		for idx, name := range patternRegex.SubexpNames() {
			if idx > 0 && name != "" && idx < len(sub) {
				captures[name] = rewrite.Capture{Name: name, Text: sub[idx]}
			}
		}
		if !checkWhereConstraints(rule.Where, captures) {
			continue
		}
		rendered, err := rewrite.RenderReplacementWithTransforms(rule.Replace, trimmed, captures, rule.Transforms)
		if err != nil {
			return source, 0, err
		}
		indentLen := len(line) - len(strings.TrimLeft(line, " \t"))
		newLine := line[:indentLen] + string(rendered)
		if newLine != line {
			lines[i] = newLine
			editsApplied++
		}
	}

	if editsApplied == 0 {
		return source, 0, nil
	}
	return []byte(strings.Join(lines, "\n")), editsApplied, nil
}

func compileSnippetToLineRegex(snippet string) (*regexp.Regexp, error) {
	metaRe := regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
	var b strings.Builder
	b.WriteString("^")
	last := 0
	locs := metaRe.FindAllStringSubmatchIndex(snippet, -1)
	for i, loc := range locs {
		literal := snippet[last:loc[0]]
		// Collapse whitespace in literal to `\s+`
		wsParts := strings.Fields(literal)
		if len(wsParts) > 0 {
			if last > 0 && (literal[0] == ' ' || literal[0] == '\t') {
				b.WriteString(`\s+`)
			}
			for j, p := range wsParts {
				if j > 0 {
					b.WriteString(`\s+`)
				}
				b.WriteString(regexp.QuoteMeta(p))
			}
			if literal[len(literal)-1] == ' ' || literal[len(literal)-1] == '\t' {
				b.WriteString(`\s+`)
			}
		} else if len(literal) > 0 {
			b.WriteString(`\s+`)
		}

		varName := snippet[loc[2]:loc[3]]
		// If this is the last token on the line, match remaining non-comment text; otherwise stop before delimiter.
		if i == len(locs)-1 && loc[1] == len(snippet) {
			b.WriteString(fmt.Sprintf(`(?P<%s>.+?)`, varName))
		} else {
			nextChar := snippet[loc[1]]
			if nextChar == ':' || nextChar == '=' || nextChar == '/' || nextChar == '@' {
				b.WriteString(fmt.Sprintf(`(?P<%s>[^\s%c]+)`, varName, nextChar))
			} else {
				b.WriteString(fmt.Sprintf(`(?P<%s>\S+)`, varName))
			}
		}
		last = loc[1]
	}
	if last < len(snippet) {
		b.WriteString(regexp.QuoteMeta(snippet[last:]))
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func applyLineRegexRule(source []byte, rule rewrite.RewriteRule) ([]byte, int, error) {
	if rule.Regex == "" {
		return source, 0, nil
	}
	re, err := regexp.Compile(rule.Regex)
	if err != nil {
		return source, 0, err
	}
	lines := strings.Split(string(source), "\n")
	edits := 0
	for i, line := range lines {
		if re.MatchString(line) {
			rendered, err := rewrite.RenderReplacementWithTransforms(rule.Replace, line, nil, rule.Transforms)
			if err != nil {
				return source, 0, err
			}
			if string(rendered) != line {
				lines[i] = string(rendered)
				edits++
			}
		}
	}
	if edits == 0 {
		return source, 0, nil
	}
	return []byte(strings.Join(lines, "\n")), edits, nil
}

func splitKeyValPattern(s string) (key string, val string, ok bool) {
	idx := strings.Index(s, ":")
	if idx <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:]), true
}

func splitPropertiesPattern(s string) (key string, val string, ok bool) {
	idx := strings.IndexAny(s, "=:")
	if idx <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:]), true
}

func matchConfigValue(patternVal string, actualVal string, captures map[string]rewrite.Capture) bool {
	if strings.HasPrefix(patternVal, "$") {
		name := strings.TrimPrefix(patternVal, "$")
		captures[name] = rewrite.Capture{Name: name, Text: actualVal}
		return true
	}
	re, err := compileSnippetToLineRegex(patternVal)
	if err != nil {
		return patternVal == actualVal
	}
	sub := re.FindStringSubmatch(actualVal)
	if sub == nil {
		return false
	}
	for idx, name := range re.SubexpNames() {
		if idx > 0 && name != "" && idx < len(sub) {
			captures[name] = rewrite.Capture{Name: name, Text: sub[idx]}
		}
	}
	return true
}

func checkWhereConstraints(where map[string]string, captures map[string]rewrite.Capture) bool {
	for k, expr := range where {
		cleanKey := strings.TrimPrefix(k, "$")
		capVal, ok := captures[cleanKey]
		if !ok {
			return false
		}
		re, err := regexp.Compile(expr)
		if err != nil || !re.MatchString(capVal.Text) {
			return false
		}
	}
	return true
}
