---
applyTo: "CLAUDE.md,.claude/rules/**,.github/copilot-instructions.md,.github/instructions/**,CONTRIBUTING.md"
---

# Reviewing changes to instruction surfaces

These files are contracts between readers, not prose. A diff here can read correctly on its own and still break a file it does not touch, so both checks below are about what depends on the edit rather than the edit itself. Both failures look like ordinary tidying in the diff, which is why they need naming.

## Two readers, and neither sees the other's files

Claude Code loads `CLAUDE.md` and `.claude/rules/`. GitHub Copilot reads `.github/copilot-instructions.md` and `.github/instructions/`. Neither loads the other's files, and `CONTRIBUTING.md` is written for human contributors and is loaded by neither.

So "the same point is still made elsewhere" holds only when the other file reaches the same reader. Establish that before accepting a removal justified by content living somewhere else.

## A reference has to resolve for whoever follows it

When a diff removes content and leaves a pointer in its place, flag it unless all three hold:

- the named file and section exist;
- that section carries the removed content in full, not a shorter gesture at it;
- and the target reaches the same reader as the file the pointer sits in.

A pointer that fails the third test deletes the guidance for that reader while appearing to relocate it.

## Loading posture is part of the contract

A file under `.claude/rules/` carrying a `paths:` key in its YAML frontmatter loads only when a file matching those globs is read; one without it loads on every turn. Adding, removing, or narrowing `paths:` changes *when* the guidance reaches the reader, and the diff shows only a few lines of frontmatter. Flag either case:

- **`paths:` added to, or narrowed on, a file containing the string `(extension point:`.** Consumers locate these supplies by reading project-level rules that are already loaded. Once the file is path-conditioned the supply is not found, and the consumer falls back to its own default with nothing reporting an error.
- **`paths:` added to a rule whose guidance governs *authoring* a kind of file.** The glob fires when a matching file is read, so such a rule stops loading at the one moment it is needed — when the file is created.

The reverse direction is not a defect: moving a rule off always-loaded is the intended way to cut per-turn cost, so flag it only where one of the two cases above applies.
