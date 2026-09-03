# PR Waste Patterns

(extension point: `pr-waste-patterns`)

Waste in a PR diff in this project means artifacts that consume reviewer attention without creating value: scaffolding left over from development, debug instrumentation that didn't get removed, conflict resolution that didn't get cleaned up. The patterns below are the source of truth for the diff scan run from this repository — a reviewer doing it by hand reads this file directly; a tooling-assisted check consumes it via the `pr-waste-patterns` extension point. GitHub Copilot's review brief is a separate surface that does not read this file and carries its own, shorter set, so a pattern stated here is not thereby in force there.

A baseline scan need only catch conflict markers in added lines; everything else lives here so this Go project can express its language-specific waste set in one place.

## Patterns to Flag in Added Lines

- **Commented-out code blocks** — heuristic: 3+ consecutive commented lines in added code. Comments explaining intent are not waste; commented-out implementations are.
- **Debug instrumentation** — `fmt.Println`, `fmt.Printf` to stdout, `log.Println`/`log.Printf` left in non-error paths, `// DEBUG` comments. (Note: this MCP server speaks JSON-RPC over stdio — stray `fmt.Print*` to stdout can corrupt the protocol stream, so flag it with extra care.)
- **`TODO` / `FIXME` / `HACK` markers** — flag for visibility, not as blockers. This project treats `TODO` as a marker for future improvement, so a `TODO` in added code is normal — but the reviewer should see it called out and decide whether it needs a follow-up issue or is intentional in-flight scaffolding.
- **Real project names, paths, or session detail in seeded observation text.** Test fixtures here are free-text observations, the one field shaped to attract exactly that. See `.claude/rules/no-personal-details.md`.

## Posture

A change without any pattern matches above is fine. The point of this list is to make the agent surface candidates the reviewer can reason about, not to gate merges.
