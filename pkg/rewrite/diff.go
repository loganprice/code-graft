package rewrite

import (
	"fmt"
	"strings"
)

// UnifiedDiff produces a clean line-by-line unified diff between original and modified source.
func UnifiedDiff(path string, original, modified []byte) string {
	if string(original) == string(modified) {
		return ""
	}
	origLines := splitLines(string(original))
	modLines := splitLines(string(modified))

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)

	// Compute simple LCS-based diff lines
	ops := diffLines(origLines, modLines)
	for _, op := range ops {
		b.WriteString(op)
		b.WriteByte('\n')
	}
	return b.String()
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func diffLines(a, b []string) []string {
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

	var out []string
	i, j := 0, 0
	for i < m && j < n {
		if a[i] == b[j] {
			out = append(out, " "+a[i])
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			out = append(out, "-"+a[i])
			i++
		} else {
			out = append(out, "+"+b[j])
			j++
		}
	}
	for ; i < m; i++ {
		out = append(out, "-"+a[i])
	}
	for ; j < n; j++ {
		out = append(out, "+"+b[j])
	}
	return out
}
