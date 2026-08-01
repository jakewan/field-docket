# Design Decisions

How value-laden design forks are settled in this project.

## Adjudicating Design Forks

(extension point: `design-fork-adjudication`)

field-docket is a general-purpose tool: it stores observations for arbitrary callers with vocabularies its author will never see. So when a design fork is value-laden — what to store, what to publish in the tool contract, which default to ship, how to shape an output — settle it by **what most open-source users of a tool like this would want**, not by what fits the author's own first use case. A fork resolved from one caller's conventions fails the general-purpose thesis the whole tool rests on.

This lens *is* the tie-break. It is the deliberate alternative to settling such a fork by reaching for the simplest, least-code, or most-general option, or by a neutral A/B canvass that hands the judgment back to the user: ask what the broad population of users wants and let that decide.

**The specific pull to resist here** is encoding a caller's taxonomy into the store. A `class` is a caller-defined string; so is a `scope_kind` and a `subject`. Guidance about what to put in them belongs in the tool description, where a calling agent reads it at call time — and a *published enum* is a legitimate middle instrument, since it catches a typo at the MCP boundary without constraining what the column can hold. What does not belong is a `CHECK` constraint or a Go `const` that decides the vocabulary, both because it is one caller's taxonomy and because the store is append-only: widening it later means SQLite's twelve-step table rebuild in a database whose triggers exist to make row rewriting impossible.

**How to apply:**

- Ground the call in the actual public landscape — survey how comparable real-world projects handle it — rather than extrapolating from the author's own conventions. A proportionate survey is enough; size the research to the decision.
- Present the grounded finding before recommending, so the reasoning is visible and redirectable.
- When new landscape data reshapes a decision, re-examine already-shipped sibling decisions for whether the finding transfers — but verify it actually does, since different semantics can keep the original choice correct. Guarding one field against silent misfiling while leaving its sibling free-form defends one half of the same hazard.

This lens applies to product- and convention-level forks. Purely mechanical or internal-engineering choices (which helper to reuse, naming, internal structure) stay ordinary engineering judgment.
