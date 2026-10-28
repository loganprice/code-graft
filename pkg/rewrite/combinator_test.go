package rewrite_test

import (
	"testing"

	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompositeRuleCombinators(t *testing.T) {
	engine := newTestEngine()

	t.Run("inside + not + has: only rewrite calls inside functions that do NOT already use context", func(t *testing.T) {
		recipeYAML := `
name: "composite-inside-not-has"
description: "Test inside and not(has) combinators"
rules:
  - id: "upgrade-query-unless-legacy-tx"
    language: "go"
    match: "$DB.Query($SQL)"
    replace: "$DB.QueryContext(ctx, $SQL)"
    inside:
      kind: "function_declaration"
      has:
        pattern: "ctx context.Context"
      not:
        has:
          pattern: "$DB.Begin()"
`
		recipe, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
		require.NoError(t, err)

		src := `package db

import "context"

func ModernHandler(ctx context.Context) {
	conn.Query("SELECT 1")
}

func LegacyTxHandler(ctx context.Context) {
	tx := conn.Begin()
	conn.Query("SELECT 2")
	_ = tx
}

func NoCtxHandler() {
	conn.Query("SELECT 3")
}
`
		expected := `package db

import "context"

func ModernHandler(ctx context.Context) {
	conn.QueryContext(ctx, "SELECT 1")
}

func LegacyTxHandler(ctx context.Context) {
	tx := conn.Begin()
	conn.Query("SELECT 2")
	_ = tx
}

func NoCtxHandler() {
	conn.Query("SELECT 3")
}
`
		res, err := engine.RunOnBytes("handler.go", []byte(src), recipe)
		require.NoError(t, err)
		assert.True(t, res.Changed)
		assert.Equal(t, 1, res.EditsApplied)
		assert.Equal(t, expected, string(res.NewSource))
	})

	t.Run("follows + precedes + metavariable unification across combinators", func(t *testing.T) {
		recipeYAML := `
name: "composite-follows-precedes"
description: "Rewrite resource.Use() only when it follows Lock($RES) and precedes Unlock($RES) on the SAME resource"
rules:
  - id: "guarded-use"
    language: "go"
    match: "useResource($RES)"
    replace: "useLockedResource($RES)"
    follows:
      pattern: "lock($RES)"
    precedes:
      pattern: "unlock($RES)"
`
		recipe, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
		require.NoError(t, err)

		src := `package main

func properlyLocked() {
	lock(resA)
	useResource(resA)
	unlock(resA)
}

func mismatchedLock() {
	lock(resA)
	useResource(resB)
	unlock(resA)
}

func unlocked() {
	useResource(resA)
}
`
		expected := `package main

func properlyLocked() {
	lock(resA)
	useLockedResource(resA)
	unlock(resA)
}

func mismatchedLock() {
	lock(resA)
	useResource(resB)
	unlock(resA)
}

func unlocked() {
	useResource(resA)
}
`
		res, err := engine.RunOnBytes("locks.go", []byte(src), recipe)
		require.NoError(t, err)
		assert.True(t, res.Changed)
		assert.Equal(t, 1, res.EditsApplied)
		assert.Equal(t, expected, string(res.NewSource))
	})

	t.Run("any + all + where regex filter on metavariables", func(t *testing.T) {
		recipeYAML := `
name: "composite-any-all-where"
description: "Match either console.log or console.debug when message variable starts with err or warn"
rules:
  - id: "ts-structured-logger"
    language: "typescript"
    any:
      - pattern: "console.log($MSG)"
      - pattern: "console.debug($MSG)"
    where:
      MSG: "^(err|warn)"
    not:
      inside:
        kind: "catch_clause"
    replace: "logger.warn($MSG)"
`
		recipe, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
		require.NoError(t, err)

		src := `function process() {
  console.log(errMessage);
  console.debug(warnDetail);
  console.log(infoMessage);
  try {
    doWork();
  } catch (err) {
    console.log(err);
  }
}
`
		expected := `function process() {
  logger.warn(errMessage);
  logger.warn(warnDetail);
  console.log(infoMessage);
  try {
    doWork();
  } catch (err) {
    console.log(err);
  }
}
`
		res, err := engine.RunOnBytes("app.ts", []byte(src), recipe)
		require.NoError(t, err)
		assert.True(t, res.Changed)
		assert.Equal(t, 2, res.EditsApplied)
		assert.Equal(t, expected, string(res.NewSource))
	})
}
