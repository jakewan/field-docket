# Comment Conventions

How comments earn their place in this repository — in any file that carries them: Go, workflow YAML, `mise.toml`, the `justfile`, `.golangci.yml`. A comment is a maintenance liability as much as an aid, so it must be worth the cost of keeping it true.

- Comments explain **why** — rationale, constraints — not **what** the code or config already says.

## Comment durability

Explaining *why* is one axis; surviving an edit made elsewhere is a second, independent one. A comment can be pure rationale and still rot — a paragraph justifying a set of bounds by doing arithmetic over them is exactly that. So a comment must not restate a fact whose authority lives somewhere else. The durable forms:

- **Name the identifier, not its value.** `capped at maxLimit` survives a retune; `capped at 500 rows` does not. In config the same rule holds: a workflow comment reading `# setup-go@v7` restates the `uses:` line right below it and goes stale the moment the pin bumps — describe what the step is for, not its pinned version. Exception: a value fixed by an external specification is itself the fact, not a restatement of one — a comment naming SQLite's `STRICT`-table version floor names the spec, and naming a local identifier instead would say less.
- **Never do arithmetic over values that can move independently** — assert it mechanically where the artifact admits a check (a test for code), so the machine recomputes what a comment can only assert. Where the artifact has no such check — a workflow or a TOML file — don't state the derived value at all; a claim nothing can recompute is the brittlest form there is. Exception: an illustrative worked example is teaching, not a claim about the current state — a walkthrough of why an off-by-one would misfire is pedagogy.
- **Name an enumeration, not its cardinality.** "the recording columns" costs nothing when a ninth is added; "the eight columns" is wrong in every comment that says it. Exception: keep the count where it carries the sentence's meaning — a test asserting each of N cases is distinct.
- **An issue or PR reference must not be load-bearing for understanding a constraint.** State the constraint; cite the issue only as provenance, if at all. A reader should never need a round trip to GitHub to learn what the code must do.
- **State an external-system assumption with its source, and with what happens if it's wrong.** Claims about SQLite's locking behavior, the driver's DSN handling, or the MCP SDK can't be verified by reading this repo and rot invisibly when the third party moves. Naming the source lets a reader re-check it; naming the failure mode tells them whether it matters — "the driver sorts this pragma first, so it is armed before the next one runs; if that changed, a cold-start open would fail rather than silently misbehave" is the shape.

A cross-reference is not a restated fact. Instruction and config files here point at each other by path, and a comment that names another file to orient a reader toward a fuller treatment is durable — provided the pointing text stands on its own without the reader following it. What the *name the identifier* rule forbids is restating the pointed-to content, not naming where it lives.
