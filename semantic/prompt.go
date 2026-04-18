package semantic

import (
	"fmt"
	"strings"

	"github.com/sjarmak/livedocs/db"
)

// systemPrompt is the system message instructing the LLM how to produce
// semantic claims.
const systemPrompt = `You are a code analysis expert. Given structural information about symbols in a Go package, generate semantic claims about each symbol.

For each symbol, produce a JSON object with these fields:
- "subject_name": the symbol name (must match exactly)
- "purpose": a one-sentence description of what this symbol is for
- "usage_pattern": how this symbol is typically used (e.g., "instantiated via constructor", "passed as option", "implements interface X for Y")
- "complexity": one of "trivial", "simple", "moderate", "complex", "very_complex"
- "stability": one of "stable", "evolving", "unstable", "deprecated"

Respond with a JSON array of objects. Only include symbols you have enough context to assess. Omit fields you are uncertain about rather than guessing.

Example response:
[
  {
    "subject_name": "PodSpec",
    "purpose": "Defines the desired state of a pod including containers, volumes, and scheduling constraints",
    "usage_pattern": "Embedded in Pod and PodTemplate objects; constructed by controllers and kubectl",
    "complexity": "complex",
    "stability": "stable"
  }
]

Respond with ONLY the JSON array, no markdown fences, no commentary.`

// buildUserPrompt constructs the user message from structural claims.
func buildUserPrompt(importPath string, symbolClaims []db.SymbolWithClaims, maxSymbols int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Package: %s\n\n", importPath)

	count := len(symbolClaims)
	if maxSymbols > 0 && count > maxSymbols {
		count = maxSymbols
	}

	for i := 0; i < count; i++ {
		sc := symbolClaims[i]
		fmt.Fprintf(&b, "Symbol: %s (kind=%s, visibility=%s)\n",
			sc.Symbol.SymbolName, sc.Symbol.Kind, sc.Symbol.Visibility)

		for _, cl := range sc.Claims {
			switch cl.Predicate {
			case "has_doc":
				fmt.Fprintf(&b, "  doc: %s\n", truncate(cl.ObjectText, 300))
			case "has_signature":
				fmt.Fprintf(&b, "  signature: %s\n", cl.ObjectText)
			case "implements":
				fmt.Fprintf(&b, "  implements: %s\n", cl.ObjectText)
			case "imports":
				fmt.Fprintf(&b, "  imports: %s\n", cl.ObjectText)
			case "encloses":
				fmt.Fprintf(&b, "  encloses: %s\n", cl.ObjectText)
			case "is_test":
				b.WriteString("  [test symbol]\n")
			case "is_generated":
				b.WriteString("  [generated code]\n")
			default:
				// defines, exports, has_kind — skip to reduce noise
			}
		}
		b.WriteByte('\n')
	}

	if maxSymbols > 0 && len(symbolClaims) > maxSymbols {
		fmt.Fprintf(&b, "(%d more symbols omitted for brevity)\n", len(symbolClaims)-maxSymbols)
	}

	return b.String()
}

// truncate shortens s to at most n bytes, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
