// Package manifest provides lossless dependency manifest updaters for Go (go.mod),
// TypeScript/JavaScript (package.json), and Python (requirements.txt / pyproject.toml).
package manifest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// DependencyUpgrade specifies a package/module dependency migration.
type DependencyUpgrade struct {
	Ecosystem  string `yaml:"ecosystem" json:"ecosystem"`   // "go", "npm"/"typescript", "python"
	OldPackage string `yaml:"oldPackage" json:"oldPackage"` // e.g. "github.com/acme/sdk"
	NewPackage string `yaml:"newPackage" json:"newPackage"` // optional rename; defaults to OldPackage
	NewVersion string `yaml:"newVersion" json:"newVersion"` // e.g. "v2.1.0" or "^2.1.0" or ">=2.1.0"
}

// ApplyUpgrade checks whether filePath matches the target manifest for u.Ecosystem
// and returns the updated file bytes and whether a change was made.
func ApplyUpgrade(filePath string, content []byte, u DependencyUpgrade) ([]byte, bool) {
	base := filepath.Base(filePath)
	newPkg := u.NewPackage
	if newPkg == "" {
		newPkg = u.OldPackage
	}

	switch strings.ToLower(u.Ecosystem) {
	case "go", "golang", "gomod":
		if base != "go.mod" {
			return content, false
		}
		return updateGoMod(content, u.OldPackage, newPkg, u.NewVersion)
	case "typescript", "ts", "javascript", "js", "npm", "node":
		if base != "package.json" {
			return content, false
		}
		return updatePackageJSON(content, u.OldPackage, newPkg, u.NewVersion)
	case "python", "py", "pip", "pypi":
		if base == "requirements.txt" || base == "pyproject.toml" {
			return updatePythonDeps(content, u.OldPackage, newPkg, u.NewVersion)
		}
		return content, false
	case "rust", "rs", "cargo":
		if base != "Cargo.toml" {
			return content, false
		}
		return updateCargoToml(content, u.OldPackage, newPkg, u.NewVersion)
	case "java", "maven", "mvn":
		if base != "pom.xml" {
			return content, false
		}
		return updatePomXML(content, u.OldPackage, newPkg, u.NewVersion)
	case "ruby", "rb", "gem", "bundler":
		if base != "Gemfile" {
			return content, false
		}
		return updateGemfile(content, u.OldPackage, newPkg, u.NewVersion)
	default:
		return content, false
	}
}

// updateGoMod rewrites matching module require lines in go.mod while preserving comments and formatting.
func updateGoMod(content []byte, oldMod, newMod, newVersion string) ([]byte, bool) {
	escaped := regexp.QuoteMeta(oldMod)
	// Matches either `require github.com/old/pkg v1.2.3` or `\tgithub.com/old/pkg v1.2.3`
	re := regexp.MustCompile(`(?m)^(\s*(?:require\s+)?)(` + escaped + `)(\s+)([^\s/]+)(.*)$`)
	changed := false
	out := re.ReplaceAllFunc(content, func(line []byte) []byte {
		sub := re.FindSubmatch(line)
		if len(sub) < 6 {
			return line
		}
		replacement := fmt.Sprintf("%s%s%s%s%s", sub[1], newMod, sub[3], newVersion, sub[5])
		if string(line) != replacement {
			changed = true
		}
		return []byte(replacement)
	})
	return out, changed
}

// updatePackageJSON rewrites `"<oldPkg>": "<oldVer>"` entries in package.json while preserving key order and indentation.
func updatePackageJSON(content []byte, oldPkg, newPkg, newVersion string) ([]byte, bool) {
	escaped := regexp.QuoteMeta(oldPkg)
	re := regexp.MustCompile(`(?m)^(\s*)"(` + escaped + `)"(\s*:\s*)"([^"]+)"(\s*,?\s*)$`)
	changed := false
	out := re.ReplaceAllFunc(content, func(line []byte) []byte {
		sub := re.FindSubmatch(line)
		if len(sub) < 6 {
			return line
		}
		replacement := fmt.Sprintf(`%s"%s"%s"%s"%s`, sub[1], newPkg, sub[3], newVersion, sub[5])
		if string(line) != replacement {
			changed = true
		}
		return []byte(replacement)
	})
	return out, changed
}

// updatePythonDeps rewrites package pins in requirements.txt or pyproject.toml.
func updatePythonDeps(content []byte, oldPkg, newPkg, newVersion string) ([]byte, bool) {
	escaped := regexp.QuoteMeta(oldPkg)
	// Normalize version specifier: if newVersion doesn't start with an operator (=, >, <, ~, ^), prepend "=="
	verSpec := newVersion
	if verSpec != "" && !strings.HasPrefix(verSpec, "=") && !strings.HasPrefix(verSpec, ">") &&
		!strings.HasPrefix(verSpec, "<") && !strings.HasPrefix(verSpec, "~") && !strings.HasPrefix(verSpec, "^") {
		verSpec = "==" + verSpec
	}

	re := regexp.MustCompile(`(?m)^(\s*["']?)(` + escaped + `)\s*([=><~^!]+[^"'\s,#]+)(\s*["']?.*)$`)
	changed := false
	out := re.ReplaceAllFunc(content, func(line []byte) []byte {
		sub := re.FindSubmatch(line)
		if len(sub) < 5 {
			return line
		}
		replacement := fmt.Sprintf("%s%s%s%s", sub[1], newPkg, verSpec, sub[4])
		if string(line) != replacement {
			changed = true
		}
		return []byte(replacement)
	})
	return out, changed
}

// updateCargoToml rewrites `<oldPkg> = "<oldVer>"` lines in Cargo.toml.
func updateCargoToml(content []byte, oldPkg, newPkg, newVersion string) ([]byte, bool) {
	escaped := regexp.QuoteMeta(oldPkg)
	re := regexp.MustCompile(`(?m)^(\s*)(` + escaped + `)(\s*=\s*)"([^"]+)"(.*)$`)
	changed := false
	out := re.ReplaceAllFunc(content, func(line []byte) []byte {
		sub := re.FindSubmatch(line)
		if len(sub) < 6 {
			return line
		}
		replacement := fmt.Sprintf(`%s%s%s"%s"%s`, sub[1], newPkg, sub[3], newVersion, sub[5])
		if string(line) != replacement {
			changed = true
		}
		return []byte(replacement)
	})
	return out, changed
}

// updatePomXML rewrites `<artifactId>oldPkg</artifactId>` and the adjacent `<version>...</version>` in pom.xml.
func updatePomXML(content []byte, oldArtifact, newArtifact, newVersion string) ([]byte, bool) {
	escaped := regexp.QuoteMeta(oldArtifact)
	re := regexp.MustCompile(`(<artifactId>\s*)(` + escaped + `)(\s*</artifactId>\s*\n\s*<version>)([^<]+)(</version>)`)
	changed := false
	out := re.ReplaceAllFunc(content, func(block []byte) []byte {
		sub := re.FindSubmatch(block)
		if len(sub) < 6 {
			return block
		}
		replacement := fmt.Sprintf("%s%s%s%s%s", sub[1], newArtifact, sub[3], newVersion, sub[5])
		if string(block) != replacement {
			changed = true
		}
		return []byte(replacement)
	})
	return out, changed
}

// updateGemfile rewrites `gem '<oldPkg>', '<oldVer>'` lines in Gemfile.
func updateGemfile(content []byte, oldGem, newGem, newVersion string) ([]byte, bool) {
	escaped := regexp.QuoteMeta(oldGem)
	re := regexp.MustCompile(`(?m)^(\s*gem\s+['"])(` + escaped + `)(['"]\s*,\s*['"])([^'"]+)(['"].*)$`)
	changed := false
	out := re.ReplaceAllFunc(content, func(line []byte) []byte {
		sub := re.FindSubmatch(line)
		if len(sub) < 6 {
			return line
		}
		replacement := fmt.Sprintf("%s%s%s%s%s", sub[1], newGem, sub[3], newVersion, sub[5])
		if string(line) != replacement {
			changed = true
		}
		return []byte(replacement)
	})
	return out, changed
}
