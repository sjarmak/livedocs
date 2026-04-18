// Package defaults provides a pre-configured extractor registry with all
// standard language extractors (Go deep, TypeScript, Python, Shell).
package defaults

import (
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/extractor/goextractor"
	"github.com/sjarmak/livedocs/extractor/lang"
	"github.com/sjarmak/livedocs/extractor/treesitter"
)

// BuildDefaultRegistry creates a Registry pre-populated with the standard
// language extractors: Go (deep), TypeScript, Python, and Shell (tree-sitter).
func BuildDefaultRegistry(repoName string) *extractor.Registry {
	registry := extractor.NewRegistry()

	goDeep := &goextractor.GoDeepExtractor{Repo: repoName}
	registry.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		DeepExtractor: goDeep,
	})

	langRegistry := lang.NewRegistry()
	tsExtractor := treesitter.New(langRegistry)

	registry.Register(extractor.LanguageConfig{
		Language:          "typescript",
		Extensions:        []string{".ts", ".tsx"},
		TreeSitterGrammar: "tree-sitter-typescript",
		FastExtractor:     tsExtractor,
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "python",
		Extensions:        []string{".py"},
		TreeSitterGrammar: "tree-sitter-python",
		FastExtractor:     tsExtractor,
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "shell",
		Extensions:        []string{".sh"},
		TreeSitterGrammar: "tree-sitter-bash",
		FastExtractor:     tsExtractor,
	})

	return registry
}
