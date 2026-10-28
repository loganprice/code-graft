package lang_test

import (
	"testing"

	clang "github.com/loganprice/code-graft/internal/lang/c"
	"github.com/loganprice/code-graft/internal/lang/cpp"
	"github.com/loganprice/code-graft/internal/lang/golang"
	"github.com/loganprice/code-graft/internal/lang/java"
	"github.com/loganprice/code-graft/internal/lang/python"
	"github.com/loganprice/code-graft/internal/lang/ruby"
	"github.com/loganprice/code-graft/internal/lang/rust"
	"github.com/loganprice/code-graft/internal/lang/typescript"
	"github.com/loganprice/code-graft/pkg/rewrite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoScopeAndImportManagement(t *testing.T) {
	provider := golang.NewProvider()
	src := `package main

import (
	"context"
	"github.com/acme/legacy-sdk/client"
	other "github.com/other/lib"
)

func run(ctx context.Context) {
	c := client.New("token")
	otherClient := other.New()

	c.GetUser(ctx, "123")
	otherClient.GetUser(ctx, "456")
}
`
	doc, err := rewrite.NewDocument("main.go", provider.Name(), provider.Language(), []byte(src))
	require.NoError(t, err)
	defer doc.Close()

	scope := provider.AnalyzeScopeInfo(doc)
	assert.Equal(t, "github.com/acme/legacy-sdk/client", scope.Imports["client"])
	assert.Equal(t, "github.com/other/lib", scope.Imports["other"])
	assert.True(t, scope.SymbolBelongsToPackage("c", "github.com/acme/legacy-sdk/client"))
	assert.False(t, scope.SymbolBelongsToPackage("otherClient", "github.com/acme/legacy-sdk/client"))

	// Test AddImport + RemoveImport across passes
	addEdit := provider.AddImport(doc, "github.com/acme/v2-sdk/client", "v2client")
	require.NotNil(t, addEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*addEdit})
	require.NoError(t, err)

	remEdit := provider.RemoveImport(doc, "github.com/acme/legacy-sdk/client")
	require.NotNil(t, remEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*remEdit})
	require.NoError(t, err)

	expected := `package main

import (
	"context"
	other "github.com/other/lib"
	v2client "github.com/acme/v2-sdk/client"
)

func run(ctx context.Context) {
	c := client.New("token")
	otherClient := other.New()

	c.GetUser(ctx, "123")
	otherClient.GetUser(ctx, "456")
}
`
	assert.Equal(t, expected, string(doc.Source))
}

func TestTypeScriptScopeAndImportManagement(t *testing.T) {
	provider := typescript.NewProvider()
	src := `import { AcmeClient } from '@acme/sdk-v1';
import { OtherClient } from '@other/sdk';

const acme = new AcmeClient();
const other = new OtherClient();
`
	doc, err := rewrite.NewDocument("index.ts", provider.Name(), provider.Language(), []byte(src))
	require.NoError(t, err)
	defer doc.Close()

	scope := provider.AnalyzeScopeInfo(doc)
	assert.True(t, scope.SymbolBelongsToPackage("AcmeClient", "@acme/sdk-v1"))
	assert.True(t, scope.SymbolBelongsToPackage("acme", "@acme/sdk-v1"))
	assert.False(t, scope.SymbolBelongsToPackage("other", "@acme/sdk-v1"))

	addEdit := provider.AddImport(doc, "@acme/sdk-v2", "AcmeV2Client")
	require.NotNil(t, addEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*addEdit})
	require.NoError(t, err)

	remEdit := provider.RemoveImport(doc, "@acme/sdk-v1")
	require.NotNil(t, remEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*remEdit})
	require.NoError(t, err)

	expected := `import { OtherClient } from '@other/sdk';
import { AcmeV2Client } from '@acme/sdk-v2';

const acme = new AcmeClient();
const other = new OtherClient();
`
	assert.Equal(t, expected, string(doc.Source))
}

func TestPythonScopeAndImportManagement(t *testing.T) {
	provider := python.NewProvider()
	src := `from acme_v1 import Client
from other_pkg import Other

acme_client = Client(api_key="secret")
other_client = Other()
`
	doc, err := rewrite.NewDocument("app.py", provider.Name(), provider.Language(), []byte(src))
	require.NoError(t, err)
	defer doc.Close()

	scope := provider.AnalyzeScopeInfo(doc)
	assert.True(t, scope.SymbolBelongsToPackage("Client", "acme_v1"))
	assert.True(t, scope.SymbolBelongsToPackage("acme_client", "acme_v1"))
	assert.False(t, scope.SymbolBelongsToPackage("other_client", "acme_v1"))

	addEdit := provider.AddImport(doc, "acme_v2", "V2Client")
	require.NotNil(t, addEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*addEdit})
	require.NoError(t, err)

	remEdit := provider.RemoveImport(doc, "acme_v1")
	require.NotNil(t, remEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*remEdit})
	require.NoError(t, err)

	expected := `from other_pkg import Other
from acme_v2 import V2Client

acme_client = Client(api_key="secret")
other_client = Other()
`
	assert.Equal(t, expected, string(doc.Source))
}

func TestJavaScopeAndImportManagement(t *testing.T) {
	provider := java.NewProvider()
	src := `package com.example.app;

import com.acme.sdk.AcmeClient;
import com.other.lib.OtherClient;

public class Service {
    public void run() {
        AcmeClient client = new AcmeClient("secret");
        OtherClient other = new OtherClient();
        client.fetchUser("123");
        other.fetchUser("456");
    }
}
`
	doc, err := rewrite.NewDocument("Service.java", provider.Name(), provider.Language(), []byte(src))
	require.NoError(t, err)
	defer doc.Close()

	scope := provider.AnalyzeScopeInfo(doc)
	assert.True(t, scope.SymbolBelongsToPackage("AcmeClient", "com.acme.sdk"))
	assert.True(t, scope.SymbolBelongsToPackage("client", "com.acme.sdk"))
	assert.False(t, scope.SymbolBelongsToPackage("other", "com.acme.sdk"))

	addEdit := provider.AddImport(doc, "com.acme.sdk.v2", "AcmeV2Client")
	require.NotNil(t, addEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*addEdit})
	require.NoError(t, err)

	remEdit := provider.RemoveImport(doc, "com.acme.sdk.AcmeClient")
	require.NotNil(t, remEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*remEdit})
	require.NoError(t, err)

	assert.Contains(t, string(doc.Source), "import com.acme.sdk.v2.AcmeV2Client;")
	assert.NotContains(t, string(doc.Source), "import com.acme.sdk.AcmeClient;")
}

func TestRustScopeAndImportManagement(t *testing.T) {
	provider := rust.NewProvider()
	src := `use acme_sdk::Client;
use other_crate::Other;

fn main() {
    let client = Client::new("secret");
    let other = Other::new();
    client.get_user("123");
}
`
	doc, err := rewrite.NewDocument("main.rs", provider.Name(), provider.Language(), []byte(src))
	require.NoError(t, err)
	defer doc.Close()

	scope := provider.AnalyzeScopeInfo(doc)
	assert.True(t, scope.SymbolBelongsToPackage("Client", "acme_sdk"))
	assert.True(t, scope.SymbolBelongsToPackage("client", "acme_sdk"))
	assert.False(t, scope.SymbolBelongsToPackage("other", "acme_sdk"))

	addEdit := provider.AddImport(doc, "acme_sdk_v2", "V2Client")
	require.NotNil(t, addEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*addEdit})
	require.NoError(t, err)

	remEdit := provider.RemoveImport(doc, "acme_sdk")
	require.NotNil(t, remEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*remEdit})
	require.NoError(t, err)

	assert.Contains(t, string(doc.Source), "use acme_sdk_v2::V2Client;")
	assert.NotContains(t, string(doc.Source), "use acme_sdk::Client;")
}

func TestRubyScopeAndImportManagement(t *testing.T) {
	provider := ruby.NewProvider()
	src := `require 'acme_sdk'
require 'other_gem'

client = AcmeSdk::Client.new('secret')
other = OtherGem::Client.new
client.get_user('123')
`
	doc, err := rewrite.NewDocument("app.rb", provider.Name(), provider.Language(), []byte(src))
	require.NoError(t, err)
	defer doc.Close()

	scope := provider.AnalyzeScopeInfo(doc)
	assert.True(t, scope.SymbolBelongsToPackage("AcmeSdk", "acme_sdk"))
	assert.True(t, scope.SymbolBelongsToPackage("client", "acme_sdk"))
	assert.False(t, scope.SymbolBelongsToPackage("other", "acme_sdk"))

	addEdit := provider.AddImport(doc, "acme_sdk_v2", "")
	require.NotNil(t, addEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*addEdit})
	require.NoError(t, err)

	remEdit := provider.RemoveImport(doc, "acme_sdk")
	require.NotNil(t, remEdit)
	_, err = doc.ApplyEdits([]rewrite.Edit{*remEdit})
	require.NoError(t, err)

	assert.Contains(t, string(doc.Source), "require 'acme_sdk_v2'")
	assert.NotContains(t, string(doc.Source), "require 'acme_sdk'\n")
}

func TestCAndCppScopeAndImportManagement(t *testing.T) {
	cProv := clang.NewProvider()
	cSrc := `#include "acme_v1.h"
#include <stdio.h>

int main() {
    acme_init(1);
    return 0;
}
`
	cDoc, err := rewrite.NewDocument("main.c", cProv.Name(), cProv.Language(), []byte(cSrc))
	require.NoError(t, err)
	defer cDoc.Close()

	cScope := cProv.AnalyzeScopeInfo(cDoc)
	assert.True(t, cScope.HasPackage("acme_v1.h"))

	addEdit := cProv.AddImport(cDoc, "acme_v2.h", "")
	require.NotNil(t, addEdit)
	_, err = cDoc.ApplyEdits([]rewrite.Edit{*addEdit})
	require.NoError(t, err)

	remEdit := cProv.RemoveImport(cDoc, "acme_v1.h")
	require.NotNil(t, remEdit)
	_, err = cDoc.ApplyEdits([]rewrite.Edit{*remEdit})
	require.NoError(t, err)

	assert.Contains(t, string(cDoc.Source), `#include "acme_v2.h"`)
	assert.NotContains(t, string(cDoc.Source), `#include "acme_v1.h"`)

	cppProv := cpp.NewProvider()
	cppSrc := `#include <acme/v1.hpp>

void run() {
    auto res = acme::connect("localhost", 8080);
}
`
	cppDoc, err := rewrite.NewDocument("main.cpp", cppProv.Name(), cppProv.Language(), []byte(cppSrc))
	require.NoError(t, err)
	defer cppDoc.Close()
	assert.True(t, cppProv.AnalyzeScopeInfo(cppDoc).HasPackage("acme/v1.hpp"))
}
