package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// grammarRegistry maps language config grammar names to tree-sitter Language objects.
var grammarRegistry = map[string]*sitter.Language{
	"tree-sitter-go":         golang.GetLanguage(),
	"tree-sitter-python":     python.GetLanguage(),
	"tree-sitter-typescript": typescript.GetLanguage(),
	"tree-sitter-bash":       bash.GetLanguage(),
}

// LookupGrammar returns the tree-sitter language for the given grammar name.
func LookupGrammar(grammarName string) (*sitter.Language, bool) {
	lang, ok := grammarRegistry[grammarName]
	return lang, ok
}
