# Recipe Authoring & Pattern Guide

This guide covers everything you need to author declarative YAML recipes in `code-graft`, from basic code-snippet patterns (`$META`, `$...ARGS`) to nested structural combinators (`inside`, `has`, `follows`, `precedes`, `all`, `any`, `not`), Tier 1 semantic scope constraints (`fromPackage`, `targetVar`), string transformations, structured configuration rewrites (`yaml`, `properties`, `dockerfile`), and dependency manifest upgrades.

---

## Table of Contents

1. [Anatomy of a YAML Recipe](#1-anatomy-of-a-yaml-recipe)
2. [Code-Snippet Patterns & Metavariables (`$META`, `$...ARGS`)](#2-code-snippet-patterns--metavariables-meta-args)
3. [Tier 1 Semantic Scope & Import Disambiguation](#3-tier-1-semantic-scope--import-disambiguation)
4. [Composite Rule Combinators (`inside`, `has`, `follows`, `precedes`, `all`, `any`, `not`)](#4-composite-rule-combinators-inside-has-follows-precedes-all-any-not)
5. [Metavariable Regex Constraints (`where`) & String Transforms (`transforms`)](#5-metavariable-regex-constraints-where--string-transforms-transforms)
6. [Configuration & Infrastructure Rules (`yaml`, `properties`, `dockerfile`)](#6-configuration--infrastructure-rules-yaml-properties-dockerfile)
7. [Dependency Manifest Rules (`manifests`)](#7-dependency-manifest-rules-manifests)

---

## 1. Anatomy of a YAML Recipe

A `code-graft` recipe is a single YAML file that can orchestrate code transformations across multiple languages, configuration files, and dependency manifests in one atomic pass.

```yaml
name: spring-boot-2-to-3-migration
description: Upgrades Spring Boot 2.x applications to Spring Boot 3.0 (Jakarta EE 10, Spring Security 6, Redis properties, Java 17 base image)

# Optional: Links this recipe to Renovate Bot postUpgradeTasks (--renovate-upgrade)
targetDependency:
  ecosystem: maven
  name: org.springframework.boot:spring-boot-starter-parent
  versionPrefix: "3."

# Optional: Template parameters (see Enterprise Platform Guide)
parameters:
  - name: JAVA_TAG
    description: Target Eclipse Temurin base image tag
    default: "17-jre-jammy"

# Optional: Repository & file-level guards
preconditions:
  requiredFiles:
    - "pom.xml"
  filePatterns:
    - "*.java"
    - "pom.xml"
    - "application*.yml"
    - "Dockerfile*"
  excludeFiles:
    - "**/target/**"

# Optional: Compose other recipes or parameterized templates
includes:
  - path: ./common_jakarta_imports.yaml

# 1. Dependency Manifest Upgrades
manifests:
  - type: pom.xml
    oldModule: javax.servlet:javax.servlet-api
    newModule: jakarta.servlet:jakarta.servlet-api
    newVersion: "6.0.0"

# 2. Source Code CST Rules
rules:
  - id: java-request-mapping-get
    language: java
    match: "@RequestMapping(value = $PATH, method = RequestMethod.GET)"
    replace: "@GetMapping($PATH)"
    addImports:
      - module: org.springframework.web.bind.annotation.GetMapping

# 3. Structured Config & Infrastructure Rules
configs:
  - id: spring-yaml-redis-move
    type: yaml
    filePattern: "application*.yml"
    renameKey:
      from: "spring.redis.host"
      to: "spring.data.redis.host"

# 4. Post-Rewrite Build & Test Verification
verify:
  commands:
    - "mvn test-compile"
  rollbackOnFailure: true

# 5. Embedded Unit Tests (--test)
tests:
  - name: converts GET RequestMapping to GetMapping
    language: java
    before: |
      package com.acme;
      import org.springframework.web.bind.annotation.RequestMapping;
      import org.springframework.web.bind.annotation.RequestMethod;
      public class OrderController {
          @RequestMapping(value = "/orders", method = RequestMethod.GET)
          public String list() { return "ok"; }
      }
    after: |
      package com.acme;
      import org.springframework.web.bind.annotation.RequestMapping;
      import org.springframework.web.bind.annotation.RequestMethod;
      import org.springframework.web.bind.annotation.GetMapping;
      public class OrderController {
          @GetMapping("/orders")
          public String list() { return "ok"; }
      }
```

---

## 2. Code-Snippet Patterns & Metavariables (`$META`, `$...ARGS`)

Instead of writing S-expression Tree-sitter queries (`(call_expression function: (selector_expression ...))`), you write patterns in the target language's native syntax.

### Single-Node Metavariables (`$NAME`)
Any uppercase identifier prefixed with `$` (matching `\$[A-Z][A-Z0-9_]*`) matches a single CST node (an expression, identifier, literal, type, or block) and binds its exact source text to `NAME`.

```yaml
- id: go-migrate-get-user
  language: go
  match: "$CLIENT.GetUser($CTX, $ID)"
  replace: "$CLIENT.FetchUser($CTX, $ID, nil)"
```

* **Whitespace & Formatting Agnostic:** Because `code-graft` compiles your `match` string into a Tree-sitter CST and unifies it structurally against the target document's CST, `$CLIENT.GetUser($CTX, $ID)` matches single-line calls, multi-line calls, and calls with arbitrary spacing.
* **Repeated Metavariable Equality:** If the same metavariable appears multiple times in `match`, the rule only matches if all occurrences have identical source text (ignoring surrounding whitespace):
  ```yaml
  # Matches `x == x` or `user.ID == user.ID`, but NOT `x == y`
  match: "$X == $X"
  ```

### Variadic Metavariables (`$...NAME`)
Any uppercase identifier prefixed with `$...` (matching `\$\.\.\.[A-Z][A-Z0-9_]*`) matches **0 or more remaining sibling nodes** in an argument list, parameter list, array, or statement block.

```yaml
- id: ts-upgrade-request
  language: typescript
  match: "$CLIENT.request($PATH, $...ARGS)"
  replace: "$CLIENT.requestV2($PATH, { retry: true }, $...ARGS)"
```

* **Zero-Argument Cleanup:** When `$...ARGS` matches **0** arguments (e.g., `client.request("/users")`), `code-graft` automatically cleans up trailing/leading commas in the rendered replacement so it produces `client.requestV2("/users", { retry: true })` instead of leaving a dangling comma.

---

## 3. Tier 1 Semantic Scope & Import Disambiguation

Pure syntax matchers suffer from false positives: if two classes or packages both define `.GetUser(ctx, id)`, a purely structural matcher will rewrite both.

`code-graft` solves this without requiring a full compiler via **Tier 1 Intra-File Scope Tracking**:

1. **Import Attribution:** For every file, the language provider parses all top-level import/include/require/use statements and maps local aliases/symbols to their canonical module path.
2. **Local Variable Attribution:** The language provider scans declarations (`:=`, `var`, `const`, `let`, field declarations, and function parameters) to attribute local variables (e.g., `c := acmesdk.NewClient(...)`) back to the imported package (`github.com/acme/sdk-go/v1`).

### Using `fromPackage` and `targetVar`

| Field | Purpose |
| :--- | :--- |
| `fromPackage` | Only apply the rule if the matched receiver/qualifier originates from this package/module path. |
| `targetVar` | Name of the metavariable (without `$`, e.g., `"CLIENT"`) whose package origin should be verified against `fromPackage`. If omitted, `code-graft` checks if `fromPackage` is imported in the file. |
| `addImports` | List of imports (`module`, optional `symbol` / `alias`) to automatically insert if the rule matches at least once in the file. |
| `removeImports` | List of module paths to automatically remove if the rule matches in the file. |

#### Example: Disambiguating Go Method Calls

```yaml
- id: go-client-get-user
  language: go
  match: "$CLIENT.GetUser($CTX, $ID)"
  replace: "$CLIENT.FetchUser($CTX, $ID, nil)"
  fromPackage: "github.com/acme/sdk-go/v1"
  targetVar: "CLIENT"
  addImports:
    - module: "github.com/acme/sdk-go/v2"
      symbol: "acmesdk"
  removeImports:
    - "github.com/acme/sdk-go/v1"
```

In a file where `acmeClient := acmesdk.NewClient(tok)` and `otherClient := otherpkg.New()`, `code-graft` rewrites `acmeClient.GetUser(ctx, id)` while leaving `otherClient.GetUser(ctx, id)` completely untouched!

---

## 4. Composite Rule Combinators (`inside`, `has`, `follows`, `precedes`, `all`, `any`, `not`)

Every rule (and nested constraint) supports arbitrarily composable relational and logical combinators with **transactional metavariable capture unification**:

### Relational Combinators

| Combinator | Meaning | Default `stopBy` |
| :--- | :--- | :--- |
| `inside` | Target node must be a descendant of an ancestor matching this constraint. | `"end"` (searches up to root; set `stopBy: "neighbor"` for immediate parent only) |
| `has` | Target node must contain a descendant matching this constraint. | `"end"` (searches entire subtree; set `stopBy: "neighbor"` for direct children only) |
| `follows` | Target node must appear after a preceding sibling matching this constraint. | `"end"` (any earlier sibling in the block; set `stopBy: "neighbor"` for immediately preceding sibling) |
| `precedes` | Target node must appear before a subsequent sibling matching this constraint. | `"end"` (any later sibling in the block; set `stopBy: "neighbor"` for immediately following sibling) |

### Logical & Atomic Combinators

| Combinator | Meaning |
| :--- | :--- |
| `all` | Logical **AND**: Every child constraint in the list must match, and their captured metavariables unify into the outer capture map. |
| `any` | Logical **OR**: At least one child constraint in the list must match. |
| `not` | Logical **NOT**: The child constraint must **not** match (evaluated with a cloned capture map so failed attempts never pollute outer bindings). |
| `pattern` | Code-snippet pattern to match against the candidate node. |
| `kind` | Exact Tree-sitter CST node kind (e.g., `function_declaration`, `call_expression`, `method_declaration`). |
| `regex` | Regular expression matched against the candidate node's source text. |

### Combinator Example: Cross-Branch Metavariable Unification

Suppose you want to rewrite `legacyFetch($URL)` **only** when:
1. It is inside a function whose first parameter is `ctx context.Context`.
2. It is **not** already inside a ` retry(...)` call.
3. It is preceded in the same block by a logger call `log.Info($MSG)`.

```yaml
- id: contextual-fetch-migration
  language: go
  match: "legacyFetch($URL)"
  replace: "modernFetch($CTX_VAR, $URL)"
  inside:
    all:
      - kind: function_declaration
      - has:
          pattern: "$CTX_VAR context.Context"
  not:
    inside:
      pattern: "retry($...ARGS)"
  follows:
    pattern: "log.Info($MSG)"
```

Notice how `$CTX_VAR` is captured in the `inside.has` ancestor constraint and seamlessly interpolated into the `replace` template `modernFetch($CTX_VAR, $URL)`!

---

## 5. Metavariable Regex Constraints (`where`) & String Transforms (`transforms`)

### Constraining Captures with `where`
Use `where` to restrict captured metavariables by regular expression (`regex`) or negation (`notRegex`):

```yaml
- id: modernize-getter-calls
  language: typescript
  match: "$OBJ.$METHOD()"
  replace: "$OBJ.$PROP_NAME"
  where:
    METHOD:
      regex: "^get[A-Z][a-zA-Z0-9]*$"
      notRegex: "^getInternal"
  transforms:
    - target: PROP_NAME
      source: METHOD
      op: trimPrefix
      arg: "get"
    - target: PROP_NAME
      source: PROP_NAME
      op: camelCase
```

With this rule:
* `user.getFirstName()` matches (`METHOD = "getFirstName"`), strips `"get"`, converts `"FirstName"` to `camelCase` (`PROP_NAME = "firstName"`), and outputs `user.firstName`.
* `user.getInternalState()` is rejected by `notRegex`.

### Supported `transforms` Operations (`op`)

Transforms execute sequentially in the order listed and can chain into existing or brand-new metavariable names (`target`):

| `op` | `arg` / `replacement` | Example (`source` = `"GetUserID"`) |
| :--- | :--- | :--- |
| `camelCase` | N/A | `"getUserId"` |
| `pascalCase` | N/A | `"GetUserId"` |
| `snakeCase` | N/A | `"get_user_id"` |
| `screamingSnakeCase` | N/A | `"GET_USER_ID"` |
| `kebabCase` | N/A | `"get-user-id"` |
| `lower` | N/A | `"getuserid"` |
| `upper` | N/A | `"GETUSERID"` |
| `trimPrefix` | `arg: "Get"` | `"UserID"` |
| `trimSuffix` | `arg: "ID"` | `"GetUser"` |
| `replace` | `arg: "<regex>"`, `replacement: "<repl>"` | Regex replacement using `regexp.ReplaceAllString` |

---

## 6. Configuration & Infrastructure Rules (`yaml`, `properties`, `dockerfile`)

Enterprise migrations frequently require updating Spring `application.yml`, Kubernetes manifests, `.properties`/`.env` files, and `Dockerfile`s alongside application code.

### 1. Lossless YAML Rules (`type: yaml`)
Uses `gopkg.in/yaml.v3`'s line/column-indexed AST to rename nested dot-path keys, update scalar values, or delete keys while preserving all comments and surrounding formatting.

```yaml
configs:
  # Rename a nested key path (moves value under the new hierarchy if needed)
  - id: spring-redis-host-rename
    type: yaml
    filePattern: "application*.yml"
    renameKey:
      from: "spring.redis.host"
      to: "spring.data.redis.host"

  # Update a scalar value at a dot-path (optionally only when oldValue matches)
  - id: k8s-ingress-api-version
    type: yaml
    filePattern: "*.yaml"
    setValue:
      key: "apiVersion"
      oldValue: "networking.k8s.io/v1beta1"
      newValue: "networking.k8s.io/v1"

  # Remove a deprecated key path
  - id: remove-legacy-flag
    type: yaml
    filePattern: "application*.yml"
    deleteKey: "acme.legacy.enabled"
```

### 2. `.properties` & `.env` Rules (`type: properties`)

```yaml
configs:
  - id: props-hibernate-dialect
    type: properties
    filePattern: "*.properties"
    setValue:
      key: "spring.jpa.database-platform"
      oldValue: "org.hibernate.dialect.PostgreSQL95Dialect"
      newValue: "org.hibernate.dialect.PostgreSQLDialect"
```

### 3. `Dockerfile` Instruction Rules (`type: dockerfile`)
Supports `$META` pattern matching and `where` regex filters across `Dockerfile` instructions (`FROM`, `RUN`, `ENV`, `EXPOSE`, `LABEL`, etc.):

```yaml
configs:
  - id: dockerfile-java-11-to-17
    type: dockerfile
    filePattern: "Dockerfile*"
    match: "FROM eclipse-temurin:$TAG"
    replace: "FROM eclipse-temurin:17-jre-jammy"
    where:
      TAG:
        regex: "^11.*"
```

---

## 7. Dependency Manifest Rules (`manifests`)

The `manifests` section upgrades package names and version constraints across 6 ecosystems:

```yaml
manifests:
  # Go (go.mod)
  - type: go.mod
    oldModule: github.com/acme/sdk-go/v1
    newModule: github.com/acme/sdk-go/v2
    newVersion: v2.4.0

  # Node / TypeScript (package.json)
  - type: package.json
    oldModule: "@acme/sdk"
    newModule: "@acme/sdk-v2"
    newVersion: "^2.4.0"

  # Python (requirements.txt / pyproject.toml)
  - type: requirements.txt
    oldModule: acme-sdk
    newModule: acme-sdk-v2
    newVersion: "2.4.0"

  # Rust (Cargo.toml)
  - type: Cargo.toml
    oldModule: acme_sdk
    newModule: acme_sdk_v2
    newVersion: "2.4.0"

  # Java / Maven (pom.xml)
  - type: pom.xml
    oldModule: javax.servlet:javax.servlet-api
    newModule: jakarta.servlet:jakarta.servlet-api
    newVersion: "6.0.0"

  # Ruby / Bundler (Gemfile)
  - type: Gemfile
    oldModule: acme_sdk
    newModule: acme_sdk_v2
    newVersion: "~> 2.4"
```
