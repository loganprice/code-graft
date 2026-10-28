package rewrite_test

import (
	"fmt"
	"testing"

	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

func goExprWrapper(snippet string) (string, func(*tree_sitter.Node) *tree_sitter.Node) {
	wrapped := fmt.Sprintf("package _p\nfunc _f() {\n_ = %s\n}\n", snippet)
	unwrap := func(root *tree_sitter.Node) *tree_sitter.Node {
		var found *tree_sitter.Node
		walkNodes(root, func(n *tree_sitter.Node) {
			if found == nil && n.Kind() == "assignment_statement" {
				right := n.ChildByFieldName("right")
				if right != nil && right.NamedChildCount() > 0 {
					found = right.NamedChild(0)
				}
			}
		})
		return found
	}
	return wrapped, unwrap
}

func TestCompileAndMatchPatternsAcrossLanguages(t *testing.T) {
	goLang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	tsLang := tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())
	pyLang := tree_sitter.NewLanguage(tree_sitter_python.Language())

	tests := []struct {
		name           string
		langName       string
		lang           *tree_sitter.Language
		wrappers       []rewrite.ExpressionWrapperFunc
		matchPattern   string
		replacePattern string
		inputSource    string
		expectedOutput string
	}{
		{
			name:           "Go SDK method call with single metavariables",
			langName:       "go",
			lang:           goLang,
			wrappers:       []rewrite.ExpressionWrapperFunc{goExprWrapper},
			matchPattern:   "$CLIENT.GetUser($CTX, $ID)",
			replacePattern: "$CLIENT.FetchUser($CTX, $ID, nil)",
			inputSource: `package main

func handler(ctx Context) {
	u1, err := apiClient.GetUser(ctx, "user-123")
	u2, err := otherClient.GetUser(ctx, getID())
}
`,
			expectedOutput: `package main

func handler(ctx Context) {
	u1, err := apiClient.FetchUser(ctx, "user-123", nil)
	u2, err := otherClient.FetchUser(ctx, getID(), nil)
}
`,
		},
		{
			name:           "TypeScript SDK call with variadic metavariable $...ARGS",
			langName:       "typescript",
			lang:           tsLang,
			wrappers:       nil,
			matchPattern:   "$CLIENT.request($ENDPOINT, $...ARGS)",
			replacePattern: "$CLIENT.v2Request({ url: $ENDPOINT }, $...ARGS)",
			inputSource: `import { Client } from '@acme/sdk';

const client = new Client();
const a = client.request('/users', { timeout: 1000 }, true);
const b = client.request('/health');
`,
			expectedOutput: `import { Client } from '@acme/sdk';

const client = new Client();
const a = client.v2Request({ url: '/users' }, { timeout: 1000 }, true);
const b = client.v2Request({ url: '/health' });
`,
		},
		{
			name:           "Python SDK function call rewrite",
			langName:       "python",
			lang:           pyLang,
			wrappers:       nil,
			matchPattern:   "$CLIENT.connect($HOST, $PORT)",
			replacePattern: "$CLIENT.connect_v2(host=$HOST, port=$PORT)",
			inputSource: `import acme

def run():
    conn = client.connect("localhost", 8080)
    return conn
`,
			expectedOutput: `import acme

def run():
    conn = client.connect_v2(host="localhost", port=8080)
    return conn
`,
		},
		{
			name:           "Repeated metavariable unification ($X == $X)",
			langName:       "go",
			lang:           goLang,
			wrappers:       []rewrite.ExpressionWrapperFunc{goExprWrapper},
			matchPattern:   "$X == $X",
			replacePattern: "true",
			inputSource: `package main

func check(a, b int) bool {
	if a == b {
		return false
	}
	return a == a
}
`,
			expectedOutput: `package main

func check(a, b int) bool {
	if a == b {
		return false
	}
	return true
}
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pat, err := rewrite.CompilePattern(tc.matchPattern, tc.langName, tc.lang, tc.wrappers)
			require.NoError(t, err)
			defer pat.Close()

			doc, err := rewrite.NewDocument("test", tc.langName, tc.lang, []byte(tc.inputSource))
			require.NoError(t, err)
			defer doc.Close()

			matches := pat.FindMatches(doc)
			t.Logf("Pattern Sexp: %s, matches: %d", pat.DebugSexp(), len(matches))
			var edits []rewrite.Edit
			for _, m := range matches {
				repl := rewrite.RenderReplacement(tc.replacePattern, m.Captures)
				edits = append(edits, doc.NodeEdit(m.Node, repl, "pattern rewrite"))
			}

			_, err = doc.ApplyEdits(edits)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedOutput, string(doc.Source))
		})
	}
}
