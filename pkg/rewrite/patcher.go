// Package rewrite provides a polyglot code rewriting engine powered by Tree-sitter.
package rewrite

import (
	"bytes"
	"fmt"
	"sort"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// Edit represents a lossless byte-span replacement in a source document.
type Edit struct {
	StartByte   uint
	EndByte     uint
	StartPoint  tree_sitter.Point
	EndPoint    tree_sitter.Point
	Replacement []byte
	Description string
}

// Document wraps a parsed source file buffer and its live Tree-sitter CST.
type Document struct {
	Path     string
	LangName string
	Language *tree_sitter.Language
	Source   []byte
	Tree     *tree_sitter.Tree
	parser   *tree_sitter.Parser
}

// NewDocument parses source code with the given Tree-sitter language and returns a live Document.
func NewDocument(path string, langName string, lang *tree_sitter.Language, source []byte) (*Document, error) {
	if lang == nil {
		return nil, fmt.Errorf("language cannot be nil for %s", path)
	}
	parser := tree_sitter.NewParser()
	if err := parser.SetLanguage(lang); err != nil {
		parser.Close()
		return nil, fmt.Errorf("setting language %s: %w", langName, err)
	}

	srcCopy := make([]byte, len(source))
	copy(srcCopy, source)

	tree := parser.Parse(srcCopy, nil)
	if tree == nil {
		parser.Close()
		return nil, fmt.Errorf("failed to parse document %s", path)
	}

	return &Document{
		Path:     path,
		LangName: langName,
		Language: lang,
		Source:   srcCopy,
		Tree:     tree,
		parser:   parser,
	}, nil
}

// Close releases underlying CGo Tree-sitter resources.
func (d *Document) Close() {
	if d.Tree != nil {
		d.Tree.Close()
		d.Tree = nil
	}
	if d.parser != nil {
		d.parser.Close()
		d.parser = nil
	}
}

// NodeEdit creates an Edit that replaces the target AST node's exact byte span.
// If the replacement spans multiple lines, subsequent lines are automatically aligned
// with the indentation of the line where targetNode begins.
func (d *Document) NodeEdit(target *tree_sitter.Node, replacement []byte, description string) Edit {
	startByte := target.StartByte()
	endByte := target.EndByte()
	startPoint := target.StartPosition()
	endPoint := target.EndPosition()

	aligned := alignIndentation(d.Source, startByte, replacement)
	return Edit{
		StartByte:   startByte,
		EndByte:     endByte,
		StartPoint:  startPoint,
		EndPoint:    endPoint,
		Replacement: aligned,
		Description: description,
	}
}

// ApplyEdits applies a batch of non-overlapping byte-span edits to the Document
// in reverse byte order (bottom-up), calling Tree.Edit for each modification and
// incrementally re-parsing the syntax tree.
func (d *Document) ApplyEdits(edits []Edit) (int, error) {
	if len(edits) == 0 {
		return 0, nil
	}

	filtered := filterAndSortEditsReverse(edits)
	applied := 0

	for _, edit := range filtered {
		if edit.StartByte > edit.EndByte || int(edit.EndByte) > len(d.Source) {
			return applied, fmt.Errorf("invalid edit byte range [%d:%d] for document of length %d", edit.StartByte, edit.EndByte, len(d.Source))
		}

		// Skip no-op edits where replacement is identical to current slice.
		if bytes.Equal(d.Source[edit.StartByte:edit.EndByte], edit.Replacement) {
			continue
		}

		startPoint := edit.StartPoint
		if startPoint == (tree_sitter.Point{}) && edit.StartByte > 0 {
			startPoint = byteOffsetToPoint(d.Source, edit.StartByte)
		}
		oldEndPoint := edit.EndPoint
		if oldEndPoint == (tree_sitter.Point{}) && edit.EndByte > 0 {
			oldEndPoint = byteOffsetToPoint(d.Source, edit.EndByte)
		}
		newEndPoint := advancePoint(startPoint, edit.Replacement)

		// Splice the replacement into d.Source
		newLen := len(d.Source) - int(edit.EndByte-edit.StartByte) + len(edit.Replacement)
		newSource := make([]byte, 0, newLen)
		newSource = append(newSource, d.Source[:edit.StartByte]...)
		newSource = append(newSource, edit.Replacement...)
		newSource = append(newSource, d.Source[edit.EndByte:]...)
		d.Source = newSource

		// Inform Tree-sitter of the exact byte & point shift
		d.Tree.Edit(&tree_sitter.InputEdit{
			StartByte:      edit.StartByte,
			OldEndByte:     edit.EndByte,
			NewEndByte:     edit.StartByte + uint(len(edit.Replacement)),
			StartPosition:  startPoint,
			OldEndPosition: oldEndPoint,
			NewEndPosition: newEndPoint,
		})
		applied++
	}

	if applied > 0 {
		newTree := d.parser.Parse(d.Source, d.Tree)
		if newTree == nil {
			return applied, fmt.Errorf("incremental re-parse failed for %s", d.Path)
		}
		d.Tree.Close()
		d.Tree = newTree
	}

	return applied, nil
}

// filterAndSortEditsReverse sorts edits by StartByte descending and drops overlapping edits
// (preferring the outer/earlier-registered edit when ranges collide).
func filterAndSortEditsReverse(edits []Edit) []Edit {
	sorted := make([]Edit, len(edits))
	copy(sorted, edits)

	// Sort ascending first to filter overlaps deterministically, then reverse.
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StartByte != sorted[j].StartByte {
			return sorted[i].StartByte < sorted[j].StartByte
		}
		return sorted[i].EndByte > sorted[j].EndByte
	})

	dedup := make([]Edit, 0, len(sorted))
	var lastEnd uint
	for i, e := range sorted {
		if i > 0 && e.StartByte < lastEnd {
			// Overlapping edit; skip
			continue
		}
		dedup = append(dedup, e)
		lastEnd = e.EndByte
	}

	// Reverse order (bottom-up) so applying an edit does not shift earlier byte offsets.
	for i, j := 0, len(dedup)-1; i < j; i, j = i+1, j-1 {
		dedup[i], dedup[j] = dedup[j], dedup[i]
	}
	return dedup
}

// alignIndentation indents lines 1..N of a multi-line replacement to match the leading
// whitespace of the line containing startByte, unless they already carry that indentation.
func alignIndentation(source []byte, startByte uint, replacement []byte) []byte {
	if !bytes.Contains(replacement, []byte("\n")) {
		return replacement
	}

	indent := leadingLineWhitespace(source, startByte)
	if len(indent) == 0 {
		return replacement
	}

	lines := bytes.Split(replacement, []byte("\n"))
	var buf bytes.Buffer
	for i, line := range lines {
		if i == 0 {
			buf.Write(line)
			continue
		}
		buf.WriteByte('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			buf.Write(indent)
		}
		buf.Write(line)
	}
	return buf.Bytes()
}

// leadingLineWhitespace returns the horizontal whitespace (' ' or '\t') from the beginning
// of the line containing byteOffset up to the first non-whitespace character on that line.
func leadingLineWhitespace(source []byte, byteOffset uint) []byte {
	if int(byteOffset) > len(source) {
		byteOffset = uint(len(source))
	}
	lineStart := int(byteOffset)
	for lineStart > 0 && source[lineStart-1] != '\n' {
		lineStart--
	}
	end := lineStart
	for end < int(byteOffset) && (source[end] == ' ' || source[end] == '\t') {
		end++
	}
	return source[lineStart:end]
}

// byteOffsetToPoint computes the zero-indexed (Row, Column) for a byte offset in source.
func byteOffsetToPoint(source []byte, target uint) tree_sitter.Point {
	if int(target) > len(source) {
		target = uint(len(source))
	}
	var row, col uint
	for i := uint(0); i < target; i++ {
		if source[i] == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return tree_sitter.Point{Row: row, Column: col}
}

// advancePoint calculates the resulting Point after inserting text starting at start.
func advancePoint(start tree_sitter.Point, text []byte) tree_sitter.Point {
	row := start.Row
	col := start.Column
	for _, b := range text {
		if b == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return tree_sitter.Point{Row: row, Column: col}
}
