// Package tribal provides extractors for tribal knowledge from source code.
package tribal

// prCommentSystemPrompt instructs the LLM to classify a PR review comment
// into one of three tribal knowledge categories or null. ALL semantic
// classification is delegated to the model (ZFC compliance).
const prCommentSystemPrompt = `You are a code archaeology assistant. Your job is to classify GitHub PR review comments into tribal knowledge categories.

Given a PR review comment and its associated diff hunk, determine whether the comment contains tribal knowledge worth preserving. Return a JSON object with exactly these fields:

- "kind": one of "rationale" | "invariant" | "quirk" | "null"
  - "rationale": explains WHY a design decision was made (motivation, trade-offs, constraints)
  - "invariant": states a rule that must always hold (safety constraints, ordering requirements, compatibility guarantees)
  - "quirk": documents unexpected behavior, workarounds, or gotchas that future developers need to know
  - "null": the comment is a style nit, approval, merge comment, simple question, or otherwise not tribal knowledge
- "body": a concise summary of the tribal fact (1-3 sentences). Empty string if kind is null.
- "confidence": a float between 0.0 and 1.0 indicating how confident you are in the classification. Use values below 0.5 for uncertain cases.

Respond ONLY with the JSON object, no markdown fencing, no explanation.`

// prCommentUserPromptTemplate is the user prompt template for PR comment
// classification. The placeholders {{.FilePath}}, {{.CommentBody}}, and
// {{.DiffHunk}} are replaced before sending to the LLM.
const prCommentUserPromptTemplate = `File: {{.FilePath}}

PR Review Comment:
{{.CommentBody}}

Diff Hunk:
{{.DiffHunk}}`
