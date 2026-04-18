package semantic

import (
	"fmt"
	"strings"

	"github.com/sjarmak/livedocs/extractor"
)

// verifySystemPrompt instructs the LLM to act as an adversarial reviewer
// of semantic claims. The reviewer challenges each claim against the
// structural evidence provided.
const verifySystemPrompt = `You are an adversarial code documentation reviewer. Your job is to challenge semantic claims about code symbols by comparing them against structural evidence.

For each claim, decide:
- "accept": the claim is well-supported by the structural evidence
- "reject": the claim contradicts the evidence or makes unsupported assertions
- "downgrade": the claim is plausible but weakly supported — reduce confidence

Respond with a JSON array. Each entry must have:
- "subject_name": the symbol name (must match exactly)
- "predicate": the claim predicate being reviewed (e.g. "purpose", "usage_pattern", "complexity", "stability")
- "verdict": one of "accept", "reject", "downgrade"
- "reason": a brief explanation (1 sentence)

Only include entries for claims you were asked to review. Do not add new claims.

Respond with ONLY the JSON array, no markdown fences, no commentary.`

// buildVerifyPrompt constructs the user message for adversarial review.
// It presents the structural evidence alongside the generated claims.
func buildVerifyPrompt(claims []extractor.Claim, structuralContext string) string {
	var b strings.Builder
	b.WriteString("## Structural Evidence\n\n")
	b.WriteString(structuralContext)
	b.WriteString("\n\n## Claims to Review\n\n")

	for _, c := range claims {
		fmt.Fprintf(&b, "- Symbol: %s, Predicate: %s, Value: %q\n",
			c.SubjectName, c.Predicate, c.ObjectText)
	}

	b.WriteString("\nFor each claim above, respond with accept/reject/downgrade and a reason.")
	return b.String()
}
