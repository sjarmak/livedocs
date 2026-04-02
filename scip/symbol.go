// Package scip implements SCIP-compatible symbol string formatting and parsing.
//
// SCIP (Source Code Intelligence Protocol) defines a canonical symbol format:
//
//	<scheme> ' ' <manager> ' ' <package> ' ' <version> ' ' <descriptor>+
//
// Descriptor suffixes encode the symbol kind:
//
//	/    namespace (Go package path component)
//	#    type (struct, interface, class)
//	.    term (variable, constant, field)
//	().  method/function
//
// Identifiers containing special characters (space, /, ., #, (, ), `)
// are escaped with backticks: `my name`. Literal backticks are doubled: `has“tick`.
//
// This package generates SCIP symbol strings as a secondary index for external
// tooling compatibility. The primary identity in live_docs is the composite key
// (repo + import_path + symbol_name).
package scip

import (
	"fmt"
	"strings"
)

// DescriptorSuffix represents the kind-encoding suffix in a SCIP descriptor.
type DescriptorSuffix string

const (
	SuffixNamespace DescriptorSuffix = "/"
	SuffixType      DescriptorSuffix = "#"
	SuffixTerm      DescriptorSuffix = "."
	SuffixMethod    DescriptorSuffix = "()."
)

// Descriptor is a single component in a SCIP symbol's descriptor chain.
type Descriptor struct {
	Name   string
	Suffix DescriptorSuffix
}

// Symbol represents a parsed SCIP symbol with its components.
type Symbol struct {
	Scheme      string
	Manager     string
	Package     string
	Version     string
	Descriptors []Descriptor
}

// SymbolKind classifies a Go symbol for descriptor suffix selection.
type SymbolKind int

const (
	KindType   SymbolKind = iota // struct, interface → #
	KindFunc                     // package-level function → ().
	KindMethod                   // method on a type → ().
	KindVar                      // variable or constant → .
	KindConst                    // constant → .
)

// Format produces the canonical SCIP symbol string representation.
func (s Symbol) Format() string {
	var b strings.Builder
	b.WriteString(s.Scheme)
	b.WriteByte(' ')
	b.WriteString(s.Manager)
	b.WriteByte(' ')
	b.WriteString(s.Package)
	b.WriteByte(' ')
	b.WriteString(s.Version)
	b.WriteByte(' ')
	for _, d := range s.Descriptors {
		b.WriteString(escapeName(d.Name))
		b.WriteString(string(d.Suffix))
	}
	return b.String()
}

// ParseSymbol parses a SCIP symbol string into its components.
func ParseSymbol(s string) (Symbol, error) {
	if s == "" {
		return Symbol{}, fmt.Errorf("scip: empty symbol string")
	}

	// Split into exactly 5 parts: scheme, manager, package, version, descriptors
	// The first 4 are space-delimited; everything after the 4th space is descriptors.
	parts := strings.SplitN(s, " ", 5)
	if len(parts) < 5 {
		return Symbol{}, fmt.Errorf("scip: symbol has %d components, need at least 5 (scheme manager package version descriptors)", len(parts))
	}

	sym := Symbol{
		Scheme:  parts[0],
		Manager: parts[1],
		Package: parts[2],
		Version: parts[3],
	}

	descriptorStr := parts[4]
	if descriptorStr == "" {
		return sym, nil
	}

	descriptors, err := parseDescriptors(descriptorStr)
	if err != nil {
		return Symbol{}, fmt.Errorf("scip: %w", err)
	}
	sym.Descriptors = descriptors

	return sym, nil
}

// FormatGoSymbol is a convenience function that builds a SCIP symbol string
// for a Go symbol. It derives namespace descriptors from the package path
// relative to the module path.
func FormatGoSymbol(modulePath, version, pkgPath, symbolName string, kind SymbolKind, ownerType string) string {
	sym := Symbol{
		Scheme:  "scip-go",
		Manager: "gomod",
		Package: modulePath,
		Version: version,
	}

	// Derive namespace descriptors from package path relative to module.
	relPath := strings.TrimPrefix(pkgPath, modulePath)
	relPath = strings.TrimPrefix(relPath, "/")
	if relPath != "" {
		for _, seg := range strings.Split(relPath, "/") {
			sym.Descriptors = append(sym.Descriptors, Descriptor{
				Name:   seg,
				Suffix: SuffixNamespace,
			})
		}
	}

	// If this is a method, add the owner type descriptor first.
	if kind == KindMethod && ownerType != "" {
		sym.Descriptors = append(sym.Descriptors, Descriptor{
			Name:   ownerType,
			Suffix: SuffixType,
		})
	}

	// Add the symbol descriptor.
	suffix := kindToSuffix(kind)
	sym.Descriptors = append(sym.Descriptors, Descriptor{
		Name:   symbolName,
		Suffix: suffix,
	})

	return sym.Format()
}

func kindToSuffix(k SymbolKind) DescriptorSuffix {
	switch k {
	case KindType:
		return SuffixType
	case KindFunc, KindMethod:
		return SuffixMethod
	case KindVar, KindConst:
		return SuffixTerm
	default:
		return SuffixTerm
	}
}

// specialChars contains characters that require backtick escaping in SCIP names.
const specialChars = " /.#()`"

// needsEscaping reports whether a descriptor name contains characters that
// require backtick escaping.
func needsEscaping(name string) bool {
	return strings.ContainsAny(name, specialChars)
}

// escapeName wraps a name in backticks if it contains special characters.
// Literal backticks within the name are doubled.
func escapeName(name string) string {
	if !needsEscaping(name) {
		return name
	}
	escaped := strings.ReplaceAll(name, "`", "``")
	return "`" + escaped + "`"
}

// parseDescriptors parses the descriptor portion of a SCIP symbol string.
func parseDescriptors(s string) ([]Descriptor, error) {
	var result []Descriptor
	i := 0
	for i < len(s) {
		name, suffix, next, err := parseOneDescriptor(s, i)
		if err != nil {
			return nil, err
		}
		result = append(result, Descriptor{Name: name, Suffix: suffix})
		i = next
	}
	return result, nil
}

// parseOneDescriptor parses a single descriptor starting at position i.
// Returns the name, suffix, next position, and any error.
func parseOneDescriptor(s string, i int) (string, DescriptorSuffix, int, error) {
	if i >= len(s) {
		return "", "", i, fmt.Errorf("unexpected end of descriptor string")
	}

	var name string
	var pos int

	if s[i] == '`' {
		// Backtick-escaped name
		n, end, err := parseEscapedName(s, i)
		if err != nil {
			return "", "", 0, err
		}
		name = n
		pos = end
	} else {
		// Unescaped name: read until a suffix character
		end := i
		for end < len(s) && !isSuffixStart(s, end) {
			end++
		}
		name = s[i:end]
		pos = end
	}

	if pos >= len(s) {
		return "", "", 0, fmt.Errorf("descriptor %q missing suffix", name)
	}

	// Parse suffix
	suffix, next, err := parseSuffix(s, pos)
	if err != nil {
		return "", "", 0, err
	}

	return name, suffix, next, nil
}

// parseEscapedName parses a backtick-escaped name starting at position i.
// Returns the unescaped name and the position after the closing backtick.
func parseEscapedName(s string, i int) (string, int, error) {
	if s[i] != '`' {
		return "", 0, fmt.Errorf("expected backtick at position %d", i)
	}
	i++ // skip opening backtick

	var b strings.Builder
	for i < len(s) {
		if s[i] == '`' {
			// Check for doubled backtick (escaped literal)
			if i+1 < len(s) && s[i+1] == '`' {
				b.WriteByte('`')
				i += 2
				continue
			}
			// Closing backtick
			return b.String(), i + 1, nil
		}
		b.WriteByte(s[i])
		i++
	}
	return "", 0, fmt.Errorf("unterminated backtick-escaped name")
}

// isSuffixStart checks if position i in s starts a descriptor suffix.
func isSuffixStart(s string, i int) bool {
	switch s[i] {
	case '/': // namespace
		return true
	case '#': // type
		return true
	case '.': // term
		return true
	case '(': // method: ().
		return i+2 < len(s) && s[i+1] == ')' && s[i+2] == '.'
	default:
		return false
	}
}

// parseSuffix parses a descriptor suffix starting at position i.
func parseSuffix(s string, i int) (DescriptorSuffix, int, error) {
	if i >= len(s) {
		return "", i, fmt.Errorf("expected suffix at position %d", i)
	}
	switch s[i] {
	case '/':
		return SuffixNamespace, i + 1, nil
	case '#':
		return SuffixType, i + 1, nil
	case '.':
		return SuffixTerm, i + 1, nil
	case '(':
		if i+2 < len(s) && s[i+1] == ')' && s[i+2] == '.' {
			return SuffixMethod, i + 3, nil
		}
		return "", i, fmt.Errorf("invalid method suffix at position %d", i)
	default:
		return "", i, fmt.Errorf("unknown suffix character %q at position %d", s[i], i)
	}
}
