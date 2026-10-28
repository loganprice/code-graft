# `code-graft` — Enterprise Polyglot Code Rewriting & Migration Platform

`code-graft` is an enterprise-grade, OpenRewrite-inspired polyglot code transformation, configuration modernization, and codebase intelligence platform written in Go and powered by the official [Tree-sitter](https://tree-sitter.github.io/tree-sitter/) bindings (`github.com/tree-sitter/go-tree-sitter`).

It enables platform engineering and security teams to author declarative YAML recipes—or **automatically learn recipes from before/after code examples and Git commits**—that losslessly migrate source code, structured configuration files, container definitions, and dependency manifests across an entire polyglot organization in milliseconds.

---

## Table of Contents

1. [Why `code-graft`? (Tree-sitter CST vs. OpenRewrite LST)](#why-code-graft-tree-sitter-cst-vs-openrewrite-lst)
2. [Supported Languages, Configs & Dependency Manifests](#supported-languages-configs--dependency-manifests)
3. [Quick Start](#quick-start)
4. [Key Platform Capabilities](#key-platform-capabilities)
5. [Complete CLI Reference](#complete-cli-reference)
6. [Documentation Index](#documentation-index)
7. [Repository Layout](#repository-layout)

---

## Why `code-graft`? (Tree-sitter CST vs. OpenRewrite LST)

| Dimension | OpenRewrite (LST) | `code-graft` (Tree-sitter CST + Tier 1 Scope) |
| :--- | :--- | :--- |
| **Language Coverage** | Deep JVM/Java support; slower ramp for non-JVM languages | **10 programming/markup languages + 3 config engines + 6 manifest formats** in a single native binary |
| **Execution Speed** | Requires full compiler/type-attribution phases (seconds to minutes) | **Sub-millisecond per file** via incremental CST parsing (`Tree.Edit` + `Parser.Parse`) without needing a build environment |
| **Formatting Preservation** | Lossless Semantic Tree (LST) | **Lossless byte-span patching** in reverse byte order—100% of untouched whitespace and comments remain byte-for-byte identical |
| **Semantic Disambiguation** | Full cross-file type attribution | **Tier 1 Intra-File Scope & Import Tracking** (`fromPackage`, `targetVar`) eliminates 90%+ of structural false positives without an external type-checker |
| **Recipe Authoring** | Imperative Java Visitor classes + YAML wrappers | **Declarative code-snippet patterns** (`$META`, `$...ARGS`), composite combinators (`inside`, `has`, `not`), **Parameterized Templates**, and **By-Example Recipe Generation** (`--learn-commit`) |

---

## Supported Languages, Configs & Dependency Manifests

### 1. Programming & Markup Languages (Tree-sitter CST)

| Language ID | File Extensions | Tier 1 Scope & Import Tracking | Automatic Import Insertion / Removal |
| :--- | :--- | :--- | :--- |
| `go` | `.go` | `import` paths & aliases; `:=`, `var`, `=`, and parameter type attribution | `import "..."` / `import alias "..."` |
| `typescript` / `javascript` | `.ts`, `.tsx`, `.js`, `.jsx`, `.mjs` | ES module named, default, and `* as` namespace imports; `const`/`let`/`var` constructor & factory tracking | `import { Sym } from '...'` / `import Sym from '...'` |
| `python` | `.py` | `import x` and `from x import y [as z]`; local assignment tracking | `from pkg import sym` / `import pkg` |
| `java` | `.java` | `import pkg.Class;` declarations; field & local variable type declarations and `new Class(...)` constructors | `import pkg.Class;` |
| `rust` | `.rs` | `use crate::path::{Sym};` declarations; `let x = Type::new(...)` bindings | `use module::Symbol;` |
| `ruby` | `.rb` | `require` / `require_relative` gem declarations; `snake_case` → `CamelCase` module attribution | `require 'gem_name'` |
| `c` | `.c`, `.h` | `#include <...>` / `#include "..."` preprocessor headers | `#include <header.h>` |
| `cpp` | `.cpp`, `.cc`, `.cxx`, `.hpp`, `.hh`, `.hxx` | `#include` headers and `using namespace` declarations | `#include <header>` |
| `json` | `.json` | Structural JSON object/pair/array CST matching | N/A |
| `html` / `xml` | `.html`, `.htm`, `.xml`, `.svg` | Structural HTML/XML element, tag, and attribute CST matching | N/A |

### 2. Enterprise Configuration & Infrastructure Files

| Config Type | File Patterns | Capabilities |
| :--- | :--- | :--- |
| `yaml` | `.yml`, `.yaml` (Spring `application.yml`, Kubernetes manifests, GitHub Actions) | Lossless line/column-indexed AST rewriting: rename nested dot-path keys (`spring.redis.host` → `spring.data.redis.host`), update scalar values, or delete keys while preserving comments |
| `properties` | `.properties`, `.env` | Rename property keys (`renameKey`), update values (`setValue`), or comment/delete deprecated keys (`deleteKey`) |
| `dockerfile` | `Dockerfile`, `Dockerfile.*`, `*.dockerfile` | Structural instruction pattern matching (`FROM $IMAGE:$TAG`, `EXPOSE`, `ENV`) with metavariable regex constraints (`where`) |

### 3. Dependency Manifests

| Ecosystem | Manifest File(s) | Capabilities |
| :--- | :--- | :--- |
| **Go** | `go.mod` | Module path upgrades (`/v1` → `/v2`) and semantic version updates |
| **Node / TypeScript** | `package.json` | Package renames and version constraint updates across `dependencies`, `devDependencies`, and `peerDependencies` |
| **Python** | `requirements.txt`, `pyproject.toml` | Package renames and version pin updates (`==`, `>=`, `~=`) |
| **Rust** | `Cargo.toml` | Crate renames and version string updates (both `crate = "1.0"` and inline table `{ version = "1.0" }`) |
| **Java / Maven** | `pom.xml` | `<groupId>:<artifactId>` renames and `<version>` tag updates |
| **Ruby / Bundler** | `Gemfile` | `gem 'name', '~> x.y'` renames and version constraint updates |

---

## Quick Start

### 1. Run Tests

```bash
go test -race ./...
```

### 2. Preview a Polyglot Migration (Dry-Run Diff)

```bash
go run ./cmd/code-graft \
  --recipe configs/examples/acme_sdk_v2.yaml \
  --dir testdata/sample-repo \
  --dry-run
```

### 3. Validate & Test a Recipe Bundle

```bash
go run ./cmd/code-graft \
  --recipe configs/examples/enterprise_modernization_bundle.yaml \
  --validate \
  --test
```

### 4. Instantiate a Parameterized Template via CLI

```bash
go run ./cmd/code-graft \
  --recipe configs/examples/templates/rename_sdk_method.yaml \
  --param LANGUAGE=go \
  --param OLD_PACKAGE=github.com/acme/sdk-go/v1 \
  --param NEW_PACKAGE=github.com/acme/sdk-go/v2 \
  --param NEW_SYMBOL=acmesdk \
  --param OLD_METHOD=GetUser \
  --param NEW_METHOD=FetchUser \
  --dir testdata/sample-repo \
  --dry-run
```

### 5. Learn a New Recipe Automatically from Before/After Examples or Git Commits

```bash
# From a Before/After file pair:
go run ./cmd/code-graft \
  --learn-before path/to/before.go \
  --learn-after path/to/after.go \
  --learn-name "migrate-sdk-v2" \
  --learn-out configs/learned_recipe.yaml

# Directly from a Git commit (e.g., HEAD):
go run ./cmd/code-graft \
  --learn-commit HEAD \
  --dir . \
  --learn-out configs/learned_from_commit.yaml
```

### 6. Run Read-Only Codebase Audit & Export Impact Reports

```bash
go run ./cmd/code-graft \
  --recipe configs/examples/acme_sdk_v2.yaml \
  --dir testdata/sample-repo \
  --audit \
  --csv audit_findings.csv \
  --markdown impact_report.md
```

---

## Key Platform Capabilities

* **Lossless Incremental CST Patching (`pkg/rewrite/patcher.go`):** Applies non-overlapping byte-span edits bottom-up (reverse byte order), aligns multi-line replacements to the target line's indentation, and calls `Tree.Edit()` + `Parser.Parse()` for incremental CST updates between rules.
* **Declarative Code-Snippet Pattern Compiler (`pkg/rewrite/pattern.go`):** Write patterns in native target-language syntax using `$META` (single node capture) and `$...ARGS` (variadic 0..N argument capture), plus metavariable string transforms (`camelCase`, `pascalCase`, `snakeCase`, `replace`, `trimPrefix`, etc.).
* **Arbitrarily Nested Rule Combinators (`pkg/rewrite/combinator.go`):** Combine relational (`inside`, `has`, `follows`, `precedes` with `stopBy`) and logical (`all`, `any`, `not`) constraints with transactional metavariable capture unification.
* **Recipe Composition, Templates & Governance (`pkg/rewrite/recipe.go`, `pkg/rewrite/governance.go`):** Compose org-wide playbooks via `includes`, build reusable **Parameterized Recipe Templates** (`parameters`, `{{PARAM}}`, `includes` with `with`), guard execution with `preconditions`, and enforce quality via `--validate` and embedded `--test` suites.
* **Data Tables & Renovate Integration (`pkg/rewrite/datatable.go`):** Emit row-level codebase telemetry (`DataTableRow`) and aggregated `ImpactSummary` reports in CSV, Markdown, or JSON, and auto-trigger migrations from Renovate Bot `postUpgradeTasks` via `--renovate-upgrade`.
* **Build & Test Verification Hooks (`pkg/rewrite/verify.go`):** Execute post-rewrite build/test commands (`go build ./...`, `npm test`, `mvn test-compile`), parse compiler errors into structured `CompilerDiagnostic` structs, and **atomically roll back** modified files if verification fails.
* **"By-Example" Recipe Generator (`pkg/rewrite/learner.go`):** Synthesize generalized, scope-safe (`fromPackage`, `targetVar`), import-aware (`addImports`, `removeImports`), and self-verified (`tests`) YAML recipes from developer before/after files or Git commits.

---

## Complete CLI Reference

```text
Usage: code-graft [flags]
```

| Flag | Description |
| :--- | :--- |
| `--recipe <path>` | Path to the YAML recipe file (or parameterized template). |
| `--dir <path>` | Target workspace directory to scan and rewrite (default `.`). |
| `--dry-run` | Compute edits and print unified diffs without modifying files on disk (default `true` unless `--write` is set). |
| `--write` | Write modified files to disk in place. |
| `--json` | Emit structured JSON output (`RunResult`, `ValidationResult`, `RecipeTestReport`, or `LearnResult`). |
| `--param KEY=VALUE` | Repeatable flag to supply or override template parameters for parameterized recipes. |
| `--validate` | Run static governance validation on the recipe (checks pattern syntax, unbound metavariables, regexes, and transforms). |
| `--test` | Execute all embedded `tests` cases inside the recipe (and any included sub-recipes) in memory. |
| `--audit` | Run in read-only audit/discovery mode: collect `dataTable` rows and `impact` telemetry without modifying files. |
| `--csv <path>` | Export the execution or audit `dataTable` rows to a CSV file (use `-` for stdout). |
| `--markdown <path>` | Export a Markdown Executive Impact & Migration Summary table (use `-` for stdout). |
| `--renovate-upgrade <spec>` | Auto-discover and run recipes matching a Renovate dependency upgrade (`<depName>@<newVersion>`). |
| `--recipes-dir <path>` | Directory to scan for matching recipes when using `--renovate-upgrade` (default `configs`). |
| `--verify-cmd <cmd>` | Repeatable post-rewrite shell command (e.g., `"go test ./..."`) to verify changes; triggers atomic rollback on failure. |
| `--no-rollback` | Keep modified files on disk even if `--verify-cmd` or `verify.commands` fails. |
| `--learn-before <path>` | Comma-separated list of "before" example file paths for the By-Example Recipe Generator. |
| `--learn-after <path>` | Comma-separated list of "after" example file paths corresponding to `--learn-before`. |
| `--learn-commit <sha>` | Git commit SHA or ref (e.g., `HEAD`) to learn a migration recipe from (`git show`). |
| `--learn-name <name>` | Recipe name to assign when generating a recipe with `--learn-before` or `--learn-commit`. |
| `--learn-out <path>` | Output file path for the generated YAML recipe (defaults to stdout). |

---

## Documentation Index

* **[Recipe Authoring & Pattern Guide](docs/recipe-authoring-guide.md):** Complete reference for YAML recipes, `$META` / `$...ARGS` patterns, relational/logical combinators (`inside`, `has`, `follows`, `precedes`, `all`, `any`, `not`), metavariable transforms, Tier 1 scope tracking, config rewriters (`yaml`, `properties`, `dockerfile`), and manifest upgraders.
* **[Enterprise Platform Guide](docs/enterprise-platform-guide.md):** Deep-dive into recipe composition (`includes`), parameterized templates (`parameters`, `with`), preconditions, governance (`--validate`, `--test`), Data Tables (`--audit`, `--csv`, `--markdown`), Renovate Bot integration (`--renovate-upgrade`), Build & Test Verification Hooks (`--verify-cmd`), and the By-Example Recipe Generator (`--learn-before`, `--learn-commit`).
* **[Architecture & Go SDK Reference](docs/architecture-and-sdk.md):** Internal architecture diagrams, incremental CST patching mechanics, pattern unification algorithms, how to register new Tree-sitter grammars, and using `pkg/rewrite` as a Go library.

---

## Repository Layout

```text
.
├── cmd/
│   └── code-graft/
│       └── main.go                  # CLI entrypoint supporting rewrite, audit, governance, renovate, verify, and learn modes
├── configs/
│   └── examples/
│       ├── acme_sdk_v2.yaml         # Polyglot Go/TS/Python SDK v1 -> v2 migration recipe
│       ├── spring_boot_2_to_3.yaml  # Spring Boot 2 -> 3 (Java + pom.xml + application.yml + Dockerfile) recipe
│       ├── enterprise_modernization_bundle.yaml # Composed enterprise playbook with embedded unit tests
│       ├── renovate.json            # Sample Renovate Bot postUpgradeTasks configuration
│       └── templates/
│           └── rename_sdk_method.yaml # Reusable parameterized recipe template
├── docs/
│   ├── recipe-authoring-guide.md    # Comprehensive YAML recipe, combinator, config, and manifest authoring reference
│   ├── enterprise-platform-guide.md # Governance, templates, Data Tables, Renovate, verification, and By-Example learning
│   └── architecture-and-sdk.md      # CST internals, incremental patching, and Go SDK API reference
├── internal/
│   ├── config/
│   │   └── config.go                # Lossless YAML, .properties/.env, and Dockerfile rewrite engines
│   ├── lang/
│   │   ├── registry.go              # LanguageProvider interface & ScopeInfo attribution model
│   │   ├── golang/                  # Go Tree-sitter provider & scope/import analyzer
│   │   ├── typescript/              # TypeScript/JS Tree-sitter provider & ES module analyzer
│   │   ├── python/                  # Python Tree-sitter provider & import analyzer
│   │   ├── java/                    # Java Tree-sitter provider & class/import analyzer
│   │   ├── rust/                    # Rust Tree-sitter provider & use/crate analyzer
│   │   ├── ruby/                    # Ruby Tree-sitter provider & require/gem analyzer
│   │   ├── c/                       # C Tree-sitter provider & #include analyzer
│   │   ├── cpp/                     # C++ Tree-sitter provider & #include/namespace analyzer
│   │   ├── json/                    # JSON Tree-sitter provider
│   │   └── html/                    # HTML/XML Tree-sitter provider
│   └── manifest/
│       └── manifest.go              # Dependency upgraders for go.mod, package.json, requirements.txt, pyproject.toml, Cargo.toml, pom.xml, Gemfile
├── pkg/
│   └── rewrite/
│       ├── patcher.go               # Document wrapper, reverse-order byte-span patcher & incremental Tree.Edit re-parser
│       ├── pattern.go               # Snippet pattern compiler ($META, $...ARGS), CST unifier & string transforms
│       ├── combinator.go            # Relational & logical CST combinators (inside, has, follows, precedes, all, any, not)
│       ├── recipe.go                # YAML recipe & parameterized template loader
│       ├── governance.go            # Static recipe validator (--validate) & in-memory test runner (--test)
│       ├── datatable.go             # Data Tables, ImpactSummary aggregation, CSV/Markdown exporters & Renovate discovery
│       ├── verify.go                # Post-rewrite verification hooks, compiler diagnostic parser & atomic rollback
│       ├── learner.go               # By-Example Recipe Generator (--learn-before/--learn-after & --learn-commit)
│       ├── engine.go                # Multi-pass polyglot execution engine
│       └── diff.go                  # Unified diff generator
└── testdata/
    └── sample-repo/                 # Polyglot fixture repository (Go, TypeScript, Python)
```
