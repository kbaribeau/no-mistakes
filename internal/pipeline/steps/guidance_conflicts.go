package steps

// supplementalGuidanceRule uses ordinary decision findings, not a new detector
// or gate. Callers add it only when usable operator guidance actually renders;
// it is fixed task discipline, outside the configurable section byte budget.
const supplementalGuidanceRule = `

Supplemental guidance:
- Operator guidance supplements trusted repository requirements; it must not override them or weaken the built-in defaults. Apply all compatible requirements, regardless of source order; order is not last-writer-wins.
- If applicable requirements cannot be reconciled, return a finding with severity "warning" and action "ask-user", naming the conflicting requirements, their source labels, and the decision needed. Do not silently choose one or drop either requirement, and do not make edits that depend on resolving the conflict before that decision.`
