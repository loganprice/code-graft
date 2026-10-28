package manifest_test

import (
	"testing"

	"github.com/loganprice/code-graft/internal/manifest"
	"github.com/stretchr/testify/assert"
)

func TestManifestUpgrades(t *testing.T) {
	t.Run("go.mod module rename and version bump", func(t *testing.T) {
		in := `module github.com/example/app

go 1.24

require (
	github.com/acme/legacy-sdk v1.4.2 // some comment
	github.com/other/pkg v0.9.0
)
`
		out, changed := manifest.ApplyUpgrade("go.mod", []byte(in), manifest.DependencyUpgrade{
			Ecosystem:  "go",
			OldPackage: "github.com/acme/legacy-sdk",
			NewPackage: "github.com/acme/sdk/v2",
			NewVersion: "v2.1.0",
		})
		assert.True(t, changed)
		assert.Equal(t, `module github.com/example/app

go 1.24

require (
	github.com/acme/sdk/v2 v2.1.0 // some comment
	github.com/other/pkg v0.9.0
)
`, string(out))
	})

	t.Run("package.json package rename and version bump", func(t *testing.T) {
		in := `{
  "name": "web-app",
  "dependencies": {
    "@acme/sdk-v1": "^1.2.0",
    "react": "^19.0.0"
  }
}
`
		out, changed := manifest.ApplyUpgrade("package.json", []byte(in), manifest.DependencyUpgrade{
			Ecosystem:  "typescript",
			OldPackage: "@acme/sdk-v1",
			NewPackage: "@acme/sdk-v2",
			NewVersion: "^2.0.0",
		})
		assert.True(t, changed)
		assert.Equal(t, `{
  "name": "web-app",
  "dependencies": {
    "@acme/sdk-v2": "^2.0.0",
    "react": "^19.0.0"
  }
}
`, string(out))
	})

	t.Run("requirements.txt and pyproject.toml upgrade", func(t *testing.T) {
		in := `# Core deps
acme-v1==1.3.0
requests>=2.31.0
`
		out, changed := manifest.ApplyUpgrade("requirements.txt", []byte(in), manifest.DependencyUpgrade{
			Ecosystem:  "python",
			OldPackage: "acme-v1",
			NewPackage: "acme-v2",
			NewVersion: ">=2.0.0",
		})
		assert.True(t, changed)
		assert.Equal(t, `# Core deps
acme-v2>=2.0.0
requests>=2.31.0
`, string(out))
	})

	t.Run("Cargo.toml, pom.xml, and Gemfile upgrades", func(t *testing.T) {
		cargoIn := `[dependencies]
acme_sdk = "1.2.0"
serde = "1.0"
`
		cargoOut, changed := manifest.ApplyUpgrade("Cargo.toml", []byte(cargoIn), manifest.DependencyUpgrade{
			Ecosystem:  "rust",
			OldPackage: "acme_sdk",
			NewPackage: "acme_sdk_v2",
			NewVersion: "2.1.0",
		})
		assert.True(t, changed)
		assert.Contains(t, string(cargoOut), `acme_sdk_v2 = "2.1.0"`)

		pomIn := `<project>
  <dependencies>
    <dependency>
      <groupId>com.acme</groupId>
      <artifactId>acme-sdk</artifactId>
      <version>1.4.0</version>
    </dependency>
  </dependencies>
</project>
`
		pomOut, changed := manifest.ApplyUpgrade("pom.xml", []byte(pomIn), manifest.DependencyUpgrade{
			Ecosystem:  "java",
			OldPackage: "acme-sdk",
			NewPackage: "acme-sdk-v2",
			NewVersion: "2.4.0",
		})
		assert.True(t, changed)
		assert.Contains(t, string(pomOut), "<artifactId>acme-sdk-v2</artifactId>")
		assert.Contains(t, string(pomOut), "<version>2.4.0</version>")

		gemIn := `source 'https://rubygems.org'
gem 'acme_sdk', '~> 1.2'
`
		gemOut, changed := manifest.ApplyUpgrade("Gemfile", []byte(gemIn), manifest.DependencyUpgrade{
			Ecosystem:  "ruby",
			OldPackage: "acme_sdk",
			NewPackage: "acme_sdk_v2",
			NewVersion: "~> 2.0",
		})
		assert.True(t, changed)
		assert.Contains(t, string(gemOut), "gem 'acme_sdk_v2', '~> 2.0'")
	})
}
