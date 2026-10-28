package rewrite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/loganprice/code-graft/internal/config"
	clang "github.com/loganprice/code-graft/internal/lang/c"
	"github.com/loganprice/code-graft/internal/lang/cpp"
	"github.com/loganprice/code-graft/internal/lang/golang"
	"github.com/loganprice/code-graft/internal/lang/html"
	"github.com/loganprice/code-graft/internal/lang/java"
	jsonlang "github.com/loganprice/code-graft/internal/lang/json"
	"github.com/loganprice/code-graft/internal/lang/python"
	"github.com/loganprice/code-graft/internal/lang/ruby"
	"github.com/loganprice/code-graft/internal/lang/rust"
	"github.com/loganprice/code-graft/internal/lang/typescript"
	"github.com/loganprice/code-graft/internal/manifest"
	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestEngine() *rewrite.Engine {
	upgrader := func(filePath string, content []byte, rule rewrite.ManifestRule) ([]byte, bool) {
		return manifest.ApplyUpgrade(filePath, content, manifest.DependencyUpgrade{
			Ecosystem:  rule.Ecosystem,
			OldPackage: rule.OldPackage,
			NewPackage: rule.NewPackage,
			NewVersion: rule.NewVersion,
		})
	}
	eng := rewrite.NewEngine(
		upgrader,
		golang.NewProvider(),
		typescript.NewProvider(),
		python.NewProvider(),
		java.NewProvider(),
		rust.NewProvider(),
		ruby.NewProvider(),
		clang.NewProvider(),
		cpp.NewProvider(),
		jsonlang.NewProvider(),
		html.NewProvider(),
	)
	eng.SetConfigRewriter(config.ApplyConfigRules)
	return eng
}

func TestEndToEndPolyglotSDKMigration(t *testing.T) {
	recipe, err := rewrite.LoadRecipeYAML(filepath.Join("..", "..", "configs", "examples", "acme_sdk_v2.yaml"))
	require.NoError(t, err)

	engine := newTestEngine()
	tmpDir := t.TempDir()

	// 1. Go fixture with both target SDK and an unrelated package that also has .GetUser()
	goInput := `package service

import (
	"context"
	acmesdk "github.com/acme/sdk-go"
	"github.com/other/usersvc"
)

// FetchProfile retrieves the user profile.
func FetchProfile(ctx context.Context, id string) {
	acmeClient := acmesdk.NewClient("secret-token")
	otherClient := usersvc.New()

	// Should be rewritten because acmeClient comes from github.com/acme/sdk-go
	acmeClient.GetUser(ctx, id)

	// Must NOT be rewritten because otherClient comes from github.com/other/usersvc
	otherClient.GetUser(ctx, id)
}
`
	goExpected := `package service

import (
	"context"
	"github.com/other/usersvc"
	acmesdk "github.com/acme/sdk-go/v2"
)

// FetchProfile retrieves the user profile.
func FetchProfile(ctx context.Context, id string) {
	acmeClient := acmesdk.NewV2Client("secret-token")
	otherClient := usersvc.New()

	// Should be rewritten because acmeClient comes from github.com/acme/sdk-go
	acmeClient.FetchUser(ctx, id, nil)

	// Must NOT be rewritten because otherClient comes from github.com/other/usersvc
	otherClient.GetUser(ctx, id)
}
`

	// 2. TypeScript fixture with both target SDK and unrelated client
	tsInput := `import { AcmeClient } from '@acme/sdk-v1';
import { InternalHttp } from './http';

const client = new AcmeClient("tok_123");
const internal = new InternalHttp();

// Both variadic and zero-variadic calls on client should be rewritten
client.request('/v1/accounts', { retries: 3 });
client.request('/v1/ping');

// Unrelated client must remain untouched
internal.request('/internal/ping');
`
	tsExpected := `import { InternalHttp } from './http';
import { AcmeV2Client } from '@acme/sdk-v2';

const client = new AcmeV2Client({ token: "tok_123" });
const internal = new InternalHttp();

// Both variadic and zero-variadic calls on client should be rewritten
client.v2Request({ path: '/v1/accounts' }, { retries: 3 });
client.v2Request({ path: '/v1/ping' });

// Unrelated client must remain untouched
internal.request('/internal/ping');
`

	// 3. Python fixture with target SDK and unrelated client
	pyInput := `from acme_sdk import AcmeClient
from legacy_store import Store

client = AcmeClient("py-secret")
store = Store()

# Rewritten
item = client.get_item("item_42")
# Untouched
cached = store.get_item("item_42")
`
	pyExpected := `from legacy_store import Store
from acme_sdk_v2 import AcmeV2Client

client = AcmeV2Client(api_token="py-secret")
store = Store()

# Rewritten
item = client.fetch_item_v2(item_id="item_42")
# Untouched
cached = store.get_item("item_42")
`

	// 4. Dependency manifests
	goModInput := `module example.com/service

go 1.24

require (
	github.com/acme/sdk-go v1.8.0
	github.com/other/usersvc v0.2.0
)
`
	goModExpected := `module example.com/service

go 1.24

require (
	github.com/acme/sdk-go/v2 v2.3.0
	github.com/other/usersvc v0.2.0
)
`

	pkgJSONInput := `{
  "name": "frontend",
  "dependencies": {
    "@acme/sdk-v1": "^1.8.0"
  }
}
`
	pkgJSONExpected := `{
  "name": "frontend",
  "dependencies": {
    "@acme/sdk-v2": "^2.3.0"
  }
}
`

	reqTxtInput := `acme-sdk==1.8.0
pydantic>=2.0.0
`
	reqTxtExpected := `acme-sdk-v2>=2.3.0
pydantic>=2.0.0
`

	javaInput := `package com.example;

import com.acme.sdk.AcmeClient;
import com.other.UserClient;

public class App {
    public void run() {
        AcmeClient client = new AcmeClient("secret");
        UserClient other = new UserClient();
        client.getUser("123");
        other.getUser("456");
    }
}
`
	javaExpected := `package com.example;

import com.other.UserClient;
import com.acme.sdk.v2.AcmeV2Client;

public class App {
    public void run() {
        AcmeClient client = new AcmeClient("secret");
        UserClient other = new UserClient();
        client.fetchUserV2("123");
        other.getUser("456");
    }
}
`

	rustInput := `use acme_sdk::Client;
use other_crate::Other;

fn main() {
    let client = Client::new();
    let other = Other::new();
    client.get_user("123");
    other.get_user("456");
}
`
	rustExpected := `use other_crate::Other;
use acme_sdk_v2::Client;

fn main() {
    let client = Client::new();
    let other = Other::new();
    client.fetch_user_v2("123");
    other.get_user("456");
}
`

	rubyInput := `require 'acme_sdk'
require 'other_gem'

client = AcmeSdk::Client.new
other = OtherGem::Client.new
client.get_user("123")
other.get_user("456")
`
	rubyExpected := `require 'other_gem'
require 'acme_sdk_v2'

client = AcmeSdk::Client.new
other = OtherGem::Client.new
client.fetch_user_v2("123")
other.get_user("456")
`

	cInput := `#include "acme_v1.h"

int main() {
    acme_init(42);
    return 0;
}
`
	cExpected := `#include "acme_v2.h"

int main() {
    acme_init_v2(42, NULL);
    return 0;
}
`

	cppInput := `#include <acme/v1.hpp>

void run() {
    acme::connect("localhost", 9000);
}
`
	cppExpected := `#include <acme/v2.hpp>

void run() {
    acme::v2::connect("localhost", 9000);
}
`

	files := map[string]string{
		"service.go":       goInput,
		"client.ts":        tsInput,
		"worker.py":        pyInput,
		"App.java":         javaInput,
		"main.rs":          rustInput,
		"script.rb":        rubyInput,
		"legacy.c":         cInput,
		"modern.cpp":       cppInput,
		"go.mod":           goModInput,
		"package.json":     pkgJSONInput,
		"requirements.txt": reqTxtInput,
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0o644))
	}

	report, err := engine.RunOnDirectory(tmpDir, recipe, true)
	require.NoError(t, err)
	assert.Equal(t, 11, report.FilesScanned)
	assert.Equal(t, 11, report.FilesChanged)

	expectedFiles := map[string]string{
		"service.go":       goExpected,
		"client.ts":        tsExpected,
		"worker.py":        pyExpected,
		"App.java":         javaExpected,
		"main.rs":          rustExpected,
		"script.rb":        rubyExpected,
		"legacy.c":         cExpected,
		"modern.cpp":       cppExpected,
		"go.mod":           goModExpected,
		"package.json":     pkgJSONExpected,
		"requirements.txt": reqTxtExpected,
	}
	for name, expected := range expectedFiles {
		actual, readErr := os.ReadFile(filepath.Join(tmpDir, name))
		require.NoError(t, readErr)
		assert.Equal(t, expected, string(actual), "mismatch in rewritten file %s", name)
	}
}

func TestSpringBoot2To3MigrationRecipe(t *testing.T) {
	recipe, err := rewrite.LoadRecipeYAML(filepath.Join("..", "..", "configs", "examples", "spring_boot_2_to_3.yaml"))
	require.NoError(t, err)

	engine := newTestEngine()
	tmpDir := t.TempDir()

	controllerInput := `package com.example.orders;

import javax.persistence.Entity;
import javax.validation.Valid;
import javax.servlet.http.HttpServletRequest;
import javax.sql.DataSource;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestMethod;
import org.springframework.web.bind.annotation.RestController;

@RestController
@Entity
public class OrderController {

    private final OrderRepository orderRepository;

    @RequestMapping(value = "/orders/{id}", method = RequestMethod.GET)
    public Order getOrder(String id, HttpServletRequest req) {
        return orderRepository.findOne(id);
    }

    @RequestMapping(value = "/orders", method = RequestMethod.POST)
    public int createOrder(@Valid Order order, ResponseEntity<String> resp) {
        return resp.getStatusCodeValue();
    }
}
`

	controllerExpected := `package com.example.orders;

import jakarta.persistence.Entity;
import jakarta.validation.Valid;
import jakarta.servlet.http.HttpServletRequest;
import javax.sql.DataSource;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestMethod;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;

@RestController
@Entity
public class OrderController {

    private final OrderRepository orderRepository;

    @GetMapping("/orders/{id}")
    public Order getOrder(String id, HttpServletRequest req) {
        return orderRepository.findById(id).orElse(null);
    }

    @PostMapping("/orders")
    public int createOrder(@Valid Order order, ResponseEntity<String> resp) {
        return resp.getStatusCode().value();
    }
}
`

	pomInput := `<project>
  <parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>2.7.18</version>
  </parent>
</project>
`
	pomExpected := `<project>
  <parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>3.3.4</version>
  </parent>
</project>
`

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "OrderController.java"), []byte(controllerInput), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "pom.xml"), []byte(pomInput), 0o644))

	report, err := engine.RunOnDirectory(tmpDir, recipe, true)
	require.NoError(t, err)
	assert.Equal(t, 2, report.FilesScanned)
	assert.Equal(t, 2, report.FilesChanged)

	actualJava, err := os.ReadFile(filepath.Join(tmpDir, "OrderController.java"))
	require.NoError(t, err)
	assert.Equal(t, controllerExpected, string(actualJava))

	actualPom, err := os.ReadFile(filepath.Join(tmpDir, "pom.xml"))
	require.NoError(t, err)
	assert.Equal(t, pomExpected, string(actualPom))
}

func TestEnterpriseConfigAndInfrastructureGrammars(t *testing.T) {
	engine := newTestEngine()

	recipeYAML := `
name: "enterprise-infra-and-config-modernization"
version: "1.0.0"
description: "Tests JSON, HTML/XML, YAML, Properties, and Dockerfile config rewrites"
rules:
  - id: "json-tsconfig-target-es2022"
    language: "json"
    match: '"target": "es2016"'
    replace: '"target": "es2022"'

  - id: "html-upgrade-legacy-cdn"
    language: "html"
    match: '<script src="https://cdn.acme.com/v1/sdk.js"></script>'
    replace: '<script src="https://cdn.acme.com/v2/sdk.min.js" defer></script>'

  - id: "yaml-spring-header-size"
    language: "yaml"
    match: "server.max-http-header-size: $SIZE"
    replace: "server.max-http-request-header-size: $SIZE"

  - id: "props-spring-redis-host"
    language: "properties"
    match: "spring.redis.host = $HOST"
    replace: "spring.data.redis.host = $HOST"

  - id: "dockerfile-temurin-21"
    language: "dockerfile"
    match: "FROM eclipse-temurin:17-$VARIANT"
    replace: "FROM eclipse-temurin:21-$VARIANT"
`
	recipe, err := rewrite.ParseRecipeYAML([]byte(recipeYAML))
	require.NoError(t, err)

	// 1. JSON Tree-sitter CST rewrite (preserving surrounding keys & comments/formatting)
	jsonIn := `{
  "compilerOptions": {
    "target": "es2016",
    "strict": true
  }
}
`
	jsonOut, err := engine.RunOnBytes("tsconfig.json", []byte(jsonIn), recipe)
	require.NoError(t, err)
	assert.True(t, jsonOut.Changed)
	assert.Contains(t, string(jsonOut.NewSource), `"target": "es2022"`)
	assert.Contains(t, string(jsonOut.NewSource), `"strict": true`)

	// 2. HTML Tree-sitter CST rewrite
	htmlIn := `<!DOCTYPE html>
<html>
  <head>
    <script src="https://cdn.acme.com/v1/sdk.js"></script>
  </head>
</html>
`
	htmlOut, err := engine.RunOnBytes("index.html", []byte(htmlIn), recipe)
	require.NoError(t, err)
	assert.True(t, htmlOut.Changed)
	assert.Contains(t, string(htmlOut.NewSource), `<script src="https://cdn.acme.com/v2/sdk.min.js" defer></script>`)

	// 3. YAML nested key-path rewrite (preserving comments and indentation)
	yamlIn := `# Application config
server:
  port: 8080
  max-http-header-size: 16KB # inline comment preserved
`
	yamlOut, err := engine.RunOnBytes("application.yml", []byte(yamlIn), recipe)
	require.NoError(t, err)
	assert.True(t, yamlOut.Changed)
	assert.Equal(t, `# Application config
server:
  port: 8080
  max-http-request-header-size: 16KB # inline comment preserved
`, string(yamlOut.NewSource))

	// 4. Properties rewrite
	propsIn := `# Redis settings
spring.redis.host = redis.internal
spring.redis.port = 6379
`
	propsOut, err := engine.RunOnBytes("application.properties", []byte(propsIn), recipe)
	require.NoError(t, err)
	assert.True(t, propsOut.Changed)
	assert.Equal(t, `# Redis settings
spring.data.redis.host = redis.internal
spring.redis.port = 6379
`, string(propsOut.NewSource))

	// 5. Dockerfile instruction rewrite
	dockerIn := `# Build stage
FROM eclipse-temurin:17-jdk-alpine
COPY target/app.jar /app.jar
`
	dockerOut, err := engine.RunOnBytes("Dockerfile", []byte(dockerIn), recipe)
	require.NoError(t, err)
	assert.True(t, dockerOut.Changed)
	assert.Equal(t, `# Build stage
FROM eclipse-temurin:21-jdk-alpine
COPY target/app.jar /app.jar
`, string(dockerOut.NewSource))
}
