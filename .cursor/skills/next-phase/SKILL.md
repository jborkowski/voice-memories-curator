---
name: next-phase
description: >-
  Assess project status and generate a metaprompt (PROMPT.md) for the next
  implementation phase. Use when the user says "next phase", "what's next",
  "generate prompt", or asks to advance the project.
---

# Next Phase — Status + Metaprompt Generator

Generate a phase-implementation metaprompt for a specified model by reading
project state and determining what comes next.

## Step 0: Gather Context

Read these files (batch all reads in parallel):

- `docs/01-plan.md` — phased plan with acceptance criteria
- `adr/00-initial.md` — architecture overview
- `adr/01-local-state-db.md` — Parquet-as-state design
- `README.md` — build / config overview
- `PROMPT.md` — previous metaprompt (if exists)

## Step 1: Determine Current Phase

Scan the codebase to decide which phases are **done**, **in-progress**, or **pending**.

### Detection heuristic

For each phase (A → F), check whether its deliverable exists beyond a placeholder:

| Phase | Done when |
|-------|-----------|
| A — Scaffold | `cmd/root.go` initialises DuckDB + config; `go build` succeeds |
| B — Detect | `internal/detect/` package exists with real logic, not just `slog.Info("placeholder")` |
| C — Process | `internal/process/` package exists with ffmpeg transcode logic |
| D — Upload | `internal/upload/` package exists with HF push logic |
| E — Homebrew | Homebrew formula file exists; `cmd/daemon.go` orchestrates phases |
| F — Integration | Test harness with synthetic fixtures exists |

Run these checks:

```
Glob: internal/detect/**/*.go
Glob: internal/process/**/*.go
Glob: internal/upload/**/*.go
Grep: "placeholder" in cmd/detect.go, cmd/process.go, cmd/upload.go, cmd/daemon.go
Glob: **/Formula/*.rb OR **/homebrew-*/**
Glob: **/*_test.go
```

Read `docs/01-plan.md` line 3 for the declared status (e.g. `Status: Phase A (not started)`).

Cross-reference declared status with actual code. If they disagree, trust the code.

### Output of this step

Produce a status summary (do NOT write it to a file yet):

```
Phase A: Scaffold           ✅ done
Phase B: Detect             ⬜ next  ← target
Phase C: Process            ⬜ pending
Phase D: Upload             ⬜ pending
Phase E: Homebrew & Ops     ⬜ pending
Phase F: Integration Tests  ⬜ pending
```

Show this summary to the user before proceeding.

## Step 2: Identify the Target Phase

The **target phase** is the first phase with status `⬜ next`.

If the user specified a phase explicitly (e.g. "generate prompt for Phase C"),
use that instead.

## Step 3: Collect Phase Details

From `docs/01-plan.md`, extract for the target phase:

- **Goal**
- **Deliverable**
- **Acceptance criteria** (verbatim)
- **Risks**
- **Open questions** (from the table at the bottom of the plan)

From the ADRs, extract any SQL snippets, schema details, or design
constraints relevant to the target phase.

## Step 4: Snapshot the Codebase

Collect the current project structure and key code that the implementing
model will need as context:

1. Use Glob to get all `.go` files and build a tree.
2. Read the files that the target phase will modify or extend (e.g. for Phase B:
   `cmd/detect.go`, `internal/db/duckdb.go`, `internal/config/config.go`).
3. Read `go.mod` for dependencies.

Summarise the codebase snapshot as a Markdown section showing:
- Project structure tree
- Key existing code (structs, interfaces, wiring) with brief inline quotes
- Dependencies

## Step 5: Generate PROMPT.md

Write `PROMPT.md` at the repo root. Use this template — fill every section
from context gathered above. Adjust the model name in the header if the user
specified one (default: the model the user provided, or "Gemini 3.1 Pro").

````markdown
# Metaprompt: Implement Phase {LETTER} ({NAME}) for Voice Memories Curator

You are implementing **Phase {LETTER}: {NAME}** of the `vmc` (Voice Memories
Curator) project — a macOS `darwin/arm64` Go binary that extracts Apple Voice
Memos and publishes them to a private Hugging Face dataset.

**{PREVIOUS_PHASES} complete.** {Brief summary of what already exists.}

---

## Your Task

{One-paragraph description of what this phase delivers.}

---

## Architecture Context

### Design Principles (from ADR-00 and ADR-01)

{Numbered list of relevant design principles.}

### {Phase}-specific Logic (from ADR-01)

{SQL snippets, data flow, or algorithm relevant to this phase.}

---

## {Schema / Data Format / API section — phase-dependent}

{Tables, column definitions, conversion formulas, etc.}

---

## Existing Codebase ({PREVIOUS_PHASES} — complete)

### Project Structure
{Tree of current .go files}

### Key Existing Code
{Quoted code blocks of structs, interfaces, and wiring the model needs.}

### Dependencies (go.mod)
{List of module dependencies.}

---

## Acceptance Criteria

{Numbered list, copied verbatim from docs/01-plan.md for the target phase,
plus any elaboration from the ADRs.}

---

## Implementation Guidance

### Where to Put Code

{Suggest package layout: new packages, files to modify.}

### {Phase-specific guidance sections}

{E.g. "DuckDB Parquet Write", "ffmpeg Invocation", "HF API Auth", etc.}

### Error Handling

{Phase-specific error scenarios and expected messages.}

### Logging

{slog fields and messages for this phase.}

---

## Constraints

- **Go only** — no shell scripts, no Python.
- **DuckDB for all data ops** — don't add a Go Parquet library.
- **darwin/arm64 only** — `main.go` has the build tag. New files do NOT need it.
- **No new dependencies** unless strictly necessary.
- **Keep placeholder subcommands unchanged** — only modify the target phase's files.

---

## Questions to Resolve Before or During Implementation

{Open questions from docs/01-plan.md relevant to this phase. Include
investigation instructions.}

---

## Deliverable

After implementation, `vmc {COMMAND}` should:
{Numbered list of observable behaviors.}

Verify your implementation compiles with `CGO_ENABLED=1 go build`.
````

## Step 6: Update Plan Status

Update line 3 of `docs/01-plan.md` to reflect the new status:

```
**Status:** Phase {LETTER} (in progress)
```

## Step 7: Surface Questions

If the target phase has open questions (from the plan's table), present them
to the user using the AskQuestion tool or as a numbered list:

> Before kicking off Phase {LETTER}, these questions are still open:
> 1. {question} — Phase {X} — {status}
> ...
> Should I make reasonable defaults, or do you want to decide now?

## Notes

- If ALL phases are done, congratulate the user and suggest running the
  integration test suite or cutting a release.
- The generated PROMPT.md should be self-contained — a model receiving only
  that file and the codebase should be able to implement the phase without
  additional context.
- Target ~200-250 lines in PROMPT.md. Enough detail to be unambiguous, short
  enough that it fits in a single prompt window.
