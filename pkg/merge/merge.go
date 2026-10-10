package merge

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MergeResult represents the output of a semantic merge operation.
type MergeResult struct {
	MergedContent []byte           `json:"-"`
	MergedString  string           `json:"merged_content,omitempty"`
	HasConflict   bool             `json:"has_conflict"`
	Conflicts     []SymbolConflict `json:"conflicts,omitempty"`
	MergedSymbols []string         `json:"merged_symbols,omitempty"`
	Method        string           `json:"method"`
}

// SymbolConflict records an unresolvable collision on a specific declaration.
type SymbolConflict struct {
	File       string `json:"file"`
	Symbol     string `json:"symbol"`
	SymbolKind string `json:"symbol_kind"`
	Ours       string `json:"ours"`
	Theirs     string `json:"theirs"`
	Base       string `json:"base"`
}

// Merge3Way executes a semantic, AST-aware 3-way merge on file contents.
func Merge3Way(filePath string, base, ours, theirs []byte) (*MergeResult, error) {
	ext := filepath.Ext(filePath)

	switch ext {
	case ".go":
		res, err := mergeGoAST(filePath, base, ours, theirs)
		if err == nil {
			return res, nil
		}
		// Fallback to block semantic if AST parsing failed (e.g. invalid syntax in wip files)
		return mergeBlockSemantic(filePath, base, ours, theirs)

	case ".ts", ".tsx", ".js", ".jsx", ".py":
		return mergeBlockSemantic(filePath, base, ours, theirs)

	default:
		return mergeLine3Way(filePath, base, ours, theirs)
	}
}

// mergeGoAST performs symbol-level AST synthesis for Go files.
func mergeGoAST(filePath string, baseBytes, oursBytes, theirsBytes []byte) (*MergeResult, error) {
	fset := token.NewFileSet()

	baseFile, _ := parser.ParseFile(fset, "base.go", baseBytes, parser.ParseComments)
	oursFile, errOurs := parser.ParseFile(fset, "ours.go", oursBytes, parser.ParseComments)
	theirsFile, errTheirs := parser.ParseFile(fset, "theirs.go", theirsBytes, parser.ParseComments)

	if errOurs != nil || errTheirs != nil {
		return nil, fmt.Errorf("parsing failed: ours=%v, theirs=%v", errOurs, errTheirs)
	}

	res := &MergeResult{
		Method: "ast_go_synthesizer",
	}

	// 1. Reconcile Package Name
	pkgName := oursFile.Name.Name
	if pkgName == "" && theirsFile.Name != nil {
		pkgName = theirsFile.Name.Name
	}

	// 2. Reconcile Imports (deduplicated union)
	importMap := make(map[string]string) // path -> alias
	recordImports := func(f *ast.File) {
		if f == nil {
			return
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			alias := ""
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			importMap[path] = alias
		}
	}

	recordImports(baseFile)
	recordImports(oursFile)
	recordImports(theirsFile)

	// 3. Extract and catalog declarations
	type declItem struct {
		name string
		kind string
		decl ast.Decl
		src  string
	}

	extractDecls := func(f *ast.File, srcBytes []byte) map[string]declItem {
		m := make(map[string]declItem)
		if f == nil {
			return m
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue // Handled separately
				}
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						key := "type:" + s.Name.Name
						m[key] = declItem{name: s.Name.Name, kind: "type", decl: d, src: renderNode(fset, d)}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							key := "val:" + name.Name
							m[key] = declItem{name: name.Name, kind: "value", decl: d, src: renderNode(fset, d)}
						}
					}
				}
			case *ast.FuncDecl:
				recv := ""
				if d.Recv != nil && len(d.Recv.List) > 0 {
					recv = renderNode(fset, d.Recv.List[0].Type) + "."
				}
				key := "func:" + recv + d.Name.Name
				m[key] = declItem{name: recv + d.Name.Name, kind: "func", decl: d, src: renderNode(fset, d)}
			}
		}
		return m
	}

	baseDecls := extractDecls(baseFile, baseBytes)
	oursDecls := extractDecls(oursFile, oursBytes)
	theirsDecls := extractDecls(theirsFile, theirsBytes)

	// Combine all unique decl keys preserving deterministic order
	allKeys := make(map[string]bool)
	var orderedKeys []string

	addKey := func(k string) {
		if !allKeys[k] {
			allKeys[k] = true
			orderedKeys = append(orderedKeys, k)
		}
	}

	for _, d := range oursFile.Decls {
		for k, v := range oursDecls {
			if v.decl == d {
				addKey(k)
			}
		}
	}
	for _, d := range theirsFile.Decls {
		for k, v := range theirsDecls {
			if v.decl == d {
				addKey(k)
			}
		}
	}

	var finalDecls []ast.Decl
	var mergedSymbols []string

	for _, key := range orderedKeys {
		baseItem, inBase := baseDecls[key]
		oursItem, inOurs := oursDecls[key]
		theirsItem, inTheirs := theirsDecls[key]

		if inOurs && !inTheirs {
			// Deleted in theirs or added in ours
			if inBase && baseItem.src == oursItem.src {
				// Theirs deleted it intentionally -> keep deleted
				continue
			}
			finalDecls = append(finalDecls, oursItem.decl)
			mergedSymbols = append(mergedSymbols, oursItem.name+" (from ours)")
			continue
		}

		if inTheirs && !inOurs {
			// Deleted in ours or added in theirs
			if inBase && baseItem.src == theirsItem.src {
				// Ours deleted it intentionally -> keep deleted
				continue
			}
			finalDecls = append(finalDecls, theirsItem.decl)
			mergedSymbols = append(mergedSymbols, theirsItem.name+" (from theirs)")
			continue
		}

		if inOurs && inTheirs {
			if oursItem.src == theirsItem.src {
				// Both have identical code
				finalDecls = append(finalDecls, oursItem.decl)
				continue
			}

			// Both modified this declaration
			if inBase {
				if oursItem.src == baseItem.src {
					// Only theirs modified it -> accept theirs
					finalDecls = append(finalDecls, theirsItem.decl)
					mergedSymbols = append(mergedSymbols, theirsItem.name+" (accepted theirs)")
					continue
				}
				if theirsItem.src == baseItem.src {
					// Only ours modified it -> accept ours
					finalDecls = append(finalDecls, oursItem.decl)
					mergedSymbols = append(mergedSymbols, oursItem.name+" (accepted ours)")
					continue
				}
			}

			// Real collision inside the same symbol
			res.HasConflict = true
			res.Conflicts = append(res.Conflicts, SymbolConflict{
				File:       filePath,
				Symbol:     oursItem.name,
				SymbolKind: oursItem.kind,
				Ours:       oursItem.src,
				Theirs:     theirsItem.src,
				Base:       baseItem.src,
			})

			// Keep ours in the output by default
			finalDecls = append(finalDecls, oursItem.decl)
		}
	}

	// 4. Construct synthesized AST File
	synthFile := &ast.File{
		Name:  ast.NewIdent(pkgName),
		Decls: []ast.Decl{},
	}

	// Add reconstructed import block
	if len(importMap) > 0 {
		importDecl := &ast.GenDecl{
			Tok: token.IMPORT,
		}
		var importPaths []string
		for p := range importMap {
			importPaths = append(importPaths, p)
		}
		sort.Strings(importPaths)

		for _, p := range importPaths {
			spec := &ast.ImportSpec{
				Path: &ast.BasicLit{
					Kind:  token.STRING,
					Value: fmt.Sprintf(`"%s"`, p),
				},
			}
			if alias := importMap[p]; alias != "" {
				spec.Name = ast.NewIdent(alias)
			}
			importDecl.Specs = append(importDecl.Specs, spec)
		}
		synthFile.Decls = append(synthFile.Decls, importDecl)
	}

	synthFile.Decls = append(synthFile.Decls, finalDecls...)

	var buf bytes.Buffer
	if err := format.Node(&buf, token.NewFileSet(), synthFile); err != nil {
		return nil, err
	}

	res.MergedContent = buf.Bytes()
	res.MergedString = buf.String()
	res.MergedSymbols = mergedSymbols

	return res, nil
}

// mergeBlockSemantic handles JS/TS/Python block merges without conflict markers.
func mergeBlockSemantic(filePath string, base, ours, theirs []byte) (*MergeResult, error) {
	res := &MergeResult{
		Method: "block_semantic_synthesizer",
	}

	oursStr := string(ours)
	theirsStr := string(theirs)
	baseStr := string(base)

	// Extract import lines
	importRe := regexp.MustCompile(`(?m)^(import\s+.*|from\s+.*import\s+.*|const\s+.*=\s*require\(.*\);?)$`)

	extractImports := func(s string) []string {
		return importRe.FindAllString(s, -1)
	}

	oursImports := extractImports(oursStr)
	theirsImports := extractImports(theirsStr)

	// Combine imports cleanly
	importSet := make(map[string]bool)
	var mergedImports []string

	for _, imp := range append(oursImports, theirsImports...) {
		clean := strings.TrimSpace(imp)
		if clean != "" && !importSet[clean] {
			importSet[clean] = true
			mergedImports = append(mergedImports, clean)
		}
	}
	sort.Strings(mergedImports)

	// Strip imports from bodies
	stripImports := func(s string) string {
		return strings.TrimSpace(importRe.ReplaceAllString(s, ""))
	}

	oursBody := stripImports(oursStr)
	theirsBody := stripImports(theirsStr)
	baseBody := stripImports(baseStr)

	if oursBody == theirsBody {
		finalContent := strings.Join(mergedImports, "\n") + "\n\n" + oursBody + "\n"
		res.MergedContent = []byte(finalContent)
		res.MergedString = finalContent
		return res, nil
	}

	// Check if one side only added functions at the bottom
	if baseBody != "" && strings.HasPrefix(oursBody, baseBody) && strings.HasPrefix(theirsBody, baseBody) {
		oursAddition := strings.TrimPrefix(oursBody, baseBody)
		theirsAddition := strings.TrimPrefix(theirsBody, baseBody)

		synthesized := baseBody + "\n" + strings.TrimSpace(oursAddition) + "\n\n" + strings.TrimSpace(theirsAddition) + "\n"
		finalContent := strings.Join(mergedImports, "\n") + "\n\n" + strings.TrimSpace(synthesized) + "\n"

		res.MergedContent = []byte(finalContent)
		res.MergedString = finalContent
		res.MergedSymbols = []string{"additive_blocks_synthesized"}
		return res, nil
	}

	// Fallback to line-based 3-way
	return mergeLine3Way(filePath, base, ours, theirs)
}

// mergeLine3Way implements a simple 3-way line merge.
func mergeLine3Way(filePath string, base, ours, theirs []byte) (*MergeResult, error) {
	oursLines := strings.Split(string(ours), "\n")
	theirsLines := strings.Split(string(theirs), "\n")

	res := &MergeResult{
		Method: "line_3way",
	}

	if string(ours) == string(theirs) {
		res.MergedContent = ours
		res.MergedString = string(ours)
		return res, nil
	}

	if len(base) == 0 {
		// New file in both, combine unique lines if non-overlapping
		var combined []string
		combined = append(combined, oursLines...)
		for _, l := range theirsLines {
			if !containsLine(oursLines, l) {
				combined = append(combined, l)
			}
		}
		merged := strings.Join(combined, "\n")
		res.MergedContent = []byte(merged)
		res.MergedString = merged
		return res, nil
	}

	// Simple non-conflicting append if one is prefix of other
	baseStr := string(base)
	oursStr := string(ours)
	theirsStr := string(theirs)

	if oursStr == baseStr {
		res.MergedContent = theirs
		res.MergedString = theirsStr
		return res, nil
	}
	if theirsStr == baseStr {
		res.MergedContent = ours
		res.MergedString = oursStr
		return res, nil
	}

	// Check if edits are purely additions
	if strings.HasPrefix(oursStr, baseStr) && strings.HasPrefix(theirsStr, baseStr) {
		diffA := strings.TrimPrefix(oursStr, baseStr)
		diffB := strings.TrimPrefix(theirsStr, baseStr)
		merged := baseStr + diffA + diffB
		res.MergedContent = []byte(merged)
		res.MergedString = merged
		return res, nil
	}

	// Flag conflict without writing messy git <<<< markers
	res.HasConflict = true
	res.Conflicts = append(res.Conflicts, SymbolConflict{
		File:       filePath,
		Symbol:     "file_content",
		SymbolKind: "text_block",
		Ours:       oursStr,
		Theirs:     theirsStr,
		Base:       baseStr,
	})
	res.MergedContent = ours
	res.MergedString = oursStr

	return res, nil
}

func containsLine(lines []string, target string) bool {
	for _, l := range lines {
		if l == target {
			return true
		}
	}
	return false
}

func renderNode(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	_ = format.Node(&buf, fset, node)
	return strings.TrimSpace(buf.String())
}
