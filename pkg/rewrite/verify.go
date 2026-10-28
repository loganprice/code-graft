package rewrite

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RunOptions configures how Engine.RunWithOptions executes a recipe across a directory.
type RunOptions struct {
	WriteInPlace      bool     // write modified files to disk
	AuditOnly         bool     // collect DataTables & impact without writing files
	VerifyCommands    []string // optional CLI override/addition to recipe.Verify.Commands
	TimeoutSeconds    int      // optional timeout override (default 300s)
	RollbackOnFailure *bool    // optional override for recipe.Verify.RollbackOnFailure
}

// VerificationResult captures the execution status, command outputs, parsed compiler
// diagnostics, and rollback state of the post-rewrite verification hook.
type VerificationResult struct {
	Attempted   bool                  `json:"attempted"`
	Passed      bool                  `json:"passed"`
	RolledBack  bool                  `json:"rolledBack"`
	Commands    []VerifyCommandResult `json:"commands,omitempty"`
	Diagnostics []CompilerDiagnostic  `json:"diagnostics,omitempty"`
}

// VerifyCommandResult records the outcome of a single build/test verification command.
type VerifyCommandResult struct {
	Command    string `json:"command"`
	ExitCode   int    `json:"exitCode"`
	Passed     bool   `json:"passed"`
	DurationMs int64  `json:"durationMs"`
	Output     string `json:"output,omitempty"`
}

// CompilerDiagnostic represents a structured compiler or test error parsed from build output
// (designed to be fed directly into an LLM repair loop or CI annotation).
type CompilerDiagnostic struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

var (
	// Matches Go / Rust / GCC / Clang / TypeScript (`path/file.ext:12:5: message` or `path/file.ext(12,5): error TS...: message`)
	colonDiagRegex = regexp.MustCompile(`(?m)^([^\s:()]+?\.(?:go|rs|ts|tsx|js|py|java|rb|c|cpp|h|hpp)):(\d+)(?::(\d+))?:\s*(.+)$`)
	tsParenRegex   = regexp.MustCompile(`(?m)^([^\s:()]+?\.(?:ts|tsx|js))\((\d+),(\d+)\):\s*(.+)$`)
	// Matches Maven javac (`[ERROR] /path/File.java:[12,5] message`)
	mavenDiagRegex = regexp.MustCompile(`(?m)^\[ERROR\]\s+([^\s:]+\.java):\[(\d+)(?:,(\d+))?\]\s*(.+)$`)
)

// runVerificationCommands executes the configured shell commands sequentially in rootDir.
// It stops at the first failing command and extracts structured CompilerDiagnostics.
func runVerificationCommands(rootDir string, commands []string, timeoutSeconds int) *VerificationResult {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	res := &VerificationResult{
		Attempted: true,
		Passed:    true,
	}

	for _, cmdStr := range commands {
		trimmed := strings.TrimSpace(cmdStr)
		if trimmed == "" {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
		start := time.Now()
		cmd := exec.CommandContext(ctx, "sh", "-c", trimmed)
		cmd.Dir = rootDir
		outBytes, err := cmd.CombinedOutput()
		durationMs := time.Since(start).Milliseconds()
		cancel()

		exitCode := 0
		passed := true
		if err != nil {
			passed = false
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		outStr := string(outBytes)
		res.Commands = append(res.Commands, VerifyCommandResult{
			Command:    trimmed,
			ExitCode:   exitCode,
			Passed:     passed,
			DurationMs: durationMs,
			Output:     outStr,
		})

		if !passed {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, parseCompilerDiagnostics(outStr)...)
			break
		}
	}

	return res
}

func parseCompilerDiagnostics(output string) []CompilerDiagnostic {
	var diags []CompilerDiagnostic
	seen := make(map[string]bool)

	addDiag := func(file string, lineStr, colStr, msg string) {
		line, _ := strconv.Atoi(lineStr)
		col, _ := strconv.Atoi(colStr)
		key := file + ":" + lineStr + ":" + msg
		if seen[key] {
			return
		}
		seen[key] = true
		diags = append(diags, CompilerDiagnostic{
			File:    strings.TrimSpace(file),
			Line:    line,
			Column:  col,
			Message: strings.TrimSpace(msg),
		})
	}

	for _, sub := range colonDiagRegex.FindAllStringSubmatch(output, -1) {
		addDiag(sub[1], sub[2], sub[3], sub[4])
	}
	for _, sub := range tsParenRegex.FindAllStringSubmatch(output, -1) {
		addDiag(sub[1], sub[2], sub[3], sub[4])
	}
	for _, sub := range mavenDiagRegex.FindAllStringSubmatch(output, -1) {
		addDiag(sub[1], sub[2], sub[3], sub[4])
	}
	return diags
}
