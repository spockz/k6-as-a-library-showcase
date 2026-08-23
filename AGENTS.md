# Global Code Guidelines

## Tone and Communication

Be direct and concise. Don't open with affirmations ("Great question!", "Sure!", "Absolutely!") or close with summaries of what you just did. Don't compliment the user's choices, questions, or code. Don't add filler phrases that pad a response without adding information. Answer the question, then stop.

Never apologize, make excuses, or reference what the user said or did to explain an omission or mistake. If something was missed, just do it. Don't say "you said X so I only did Y" or "my bad for not catching that" - fix it silently and move on.

Avoid first-person self-referential narration. Don't use phrases like "Now I need to", "Let me", "I'll", "I'm going to", or similar constructions that describe your own actions as you take them. State results and decisions directly instead.

Never narrate the conversation history or the user's instructions back at them. Don't say things like "Now the user wants fix #2 after all" or "Since you asked me to do X" - just do the work without meta-commentary about what was asked or when priorities shifted.

## Read Project Documentation First

Before planning or writing any code in a project, read available project documentation to understand the codebase, conventions, and contribution expectations. Look for and read files such as `README` (any extension or none), `CONTRIBUTING`, `AGENTS`, `CLAUDE.md`, `docs/`, architecture documents, and any other project-level docs present in the repository — regardless of file extension (`.md`, `.rst`, `.txt`, plain files, etc.). This ensures plans and implementations align with the project's established patterns, constraints, and guidelines rather than making assumptions that conflict with documented decisions.

## Shell and Command-Line Safety

Always quote paths and variables in shell commands and Makefiles, even if they appear not to contain spaces (`"$var"`, not `$var`). Quote command substitutions too (`"$(cmd)"`, not `$(cmd)`). When piping `find` to `xargs`, always use null-terminated output to handle whitespace and special characters safely (`find ... -print0 | xargs -0 ...`). Use `"$@"` (not `$*`) when forwarding arguments to preserve argument boundaries. Use `--` to separate options from file arguments to guard against filenames starting with `-` (e.g. `rm -- "$f"`). Start bash scripts with `set -euo pipefail` to catch unset variables, failed commands, and pipeline failures. Apply the same discipline in Makefiles, CI scripts, and anywhere shell expansion occurs.

## Network Requests

Always check the HTTP status and raise/throw on failure — never silently swallow errors. Never return a value from a failed network call that could be mistaken for a successful empty or null response (e.g. returning `None`, `""`, `[]`, or `{}` on error). This is especially critical for expensive or stateful calls such as LLM API requests, where a silent failure wastes money and corrupts downstream logic. Prefer raising an exception or returning a typed error so the caller is forced to handle the failure explicitly.

This principle applies to regular method and function calls too: don't return a sentinel value (null, empty string, zero, empty collection) to signal failure when that same value could also be a valid successful result. Raise an exception or return a typed error/result type instead, so failures are always unambiguous to callers.

## Architecture and Code Style

Do not duplicate code (reasonable and subjective judgement is required here) if duplicating a routine/block/function/etc seems like a good idea you must surface that to a user, do not silently duplicate multiple lines of code.

## Linting and Static Analysis

Use linters, type checkers, and static analysis tools wherever available (e.g. `mypy`/`pyright` for Python, `eslint`/`tsc` for JS/TS, `go vet`/`staticcheck` for Go, `shellcheck` for shell scripts). For languages not listed here, research the standard tooling or ask the user what they prefer before proceeding. Prefer catching errors statically over discovering them at runtime. When adding new code, ensure it passes existing linter/checker configuration without warnings.

## Code Navigation & Editing

Prefer language-aware tools over text-based ones (grep/sed/awk) for understanding and modifying code.

- Use an LSP server for go-to-definition, find-references, hover types, rename, and diagnostics. Check if it's installed before starting; if missing, install it (`<install command>`) and verify (`<server> --version`).
- Use static analysis / AST tools for structural search and edits:
  - Search: `ast-grep` (semantic pattern matching) instead of `grep` for code structure.
  - Refactor/rename: LSP rename or `ast-grep --rewrite` instead of `sed`.
  - Lint/types: the project's linter and type checker (`<linter>`, `<typechecker>`) for diagnostics.
- Reserve grep/sed for non-code text (logs, config, plain strings) or quick one-off lookups where structure doesn't matter.
- Rationale: text tools miss scope, shadowing, and syntax boundaries; semantic tools understand the code's structure and won't produce malformed edits.

## Language Server (LSP)

Use an LSP server for code navigation, diagnostics, and refactoring rather than relying on text search alone.

- Check whether the LSP server is installed before starting work.
- If missing, install it: `<install command>`
- Verify it runs: `<server> --version`
- Use it for: go-to-definition, find-references, hover types, rename, and diagnostics.

## Fix Root Causes, Not Symptoms

When something is broken or awkward, fix the underlying cause rather than layering workarounds on top. A growing stack of compensating hacks is a signal that the core issue hasn't been addressed. Prefer a clean fix at the source over an increasingly complex series of patches that treat symptoms — the latter compounds complexity and makes the system harder to reason about over time.

When debugging, apply the 5 Whys: ask "why?" about the failure, then ask "why?" about that answer, repeating until you reach a level where the cause is actionable. You don't always need to go to the absolute deepest root - stop when you have enough understanding to present the user with concrete fix options at different levels, and let them decide where to intervene.

## Return Values

Prefer returning a single meaningful value or a structure with named elements (object, dataclass, struct, hash, named tuple) over returning a plain tuple or positional array. Positional returns force callers to remember order and make destructuring fragile — named elements make the meaning self-evident at the call site. (See Rich Hickey's "Simple Made Easy": complecting multiple values into a positional sequence introduces incidental complexity.)

## Error Handling

Prefer raising/throwing exceptions when an error occurs rather than returning error codes, sentinel values, or success flags. Exceptions make failures impossible to silently ignore and keep the happy path uncluttered. Only return a result-style type (e.g. `Result`/`Either`) when errors are a routine, expected part of the API contract and callers must handle both cases explicitly — not as a general substitute for exceptions.

Never let errors fail silently. Any place where an error is swallowed, unchecked, or unhandled must be addressed: fix it, log it, surface it to the user, or at minimum leave a visible note explaining why it is intentionally ignored. Silent failures hide bugs and make systems hard to diagnose.

When relying on a library call to raise on failure, verify that it actually does - check the docs or source. If it doesn't, add an explicit check or callback after the call to catch and handle the failure yourself.

## Missing vs. Null vs. Empty

Treat missing, null/nil, and falsey empty values as distinct states — conflating them is a common source of subtle bugs. A missing key in a dict/map means the data was never provided; `null`/`nil` means it was explicitly set to nothing; an empty collection (`[]`, `{}`, `""`) means it was provided but contains no elements. Never use `if x:` or truthiness checks when you need to distinguish between these states — check explicitly (e.g. in Python: `x is None` vs `len(x) == 0` vs `x not in data`; the specific syntax varies by language but the principle holds universally). Design APIs and data structures to preserve this distinction rather than collapsing it.

## Communicating Tradeoffs

When making a decision that involves meaningful tradeoffs — choosing between approaches, libraries, data structures, or architectural patterns — explain the tradeoffs clearly rather than just presenting a conclusion. Call out what is gained and what is sacrificed (performance, simplicity, flexibility, safety, etc.), and flag when a choice is context-dependent so the user can make an informed decision or redirect if their priorities differ.

## Generating Structured Data

When generating messages or payloads in a structured format (JSON, YAML, etc.), prefer building native data structures (e.g. dicts in Python, objects/maps in JS/TS, structs or maps in Go, hashes in Ruby) and serializing them, rather than constructing the output via string concatenation.

## Testing

Tests should assert observable behavior, not implementation details. Avoid testing private internals or mocking at a granularity that would let a broken implementation still pass. A test that passes when the feature is broken is worse than no test. Prefer fewer, higher-confidence tests over many shallow ones that only verify that code was called.

## Comments

Default to writing no comments. Only add one when the why is non-obvious: a hidden constraint, a subtle invariant, a workaround for a specific bug, or behavior that would surprise a reader. If removing the comment wouldn't confuse a future reader, don't write it.

Never describe what the code does - well-named identifiers already do that. Never use large decorative text blocks, banners, section dividers, or multi-line comment headers. Keep comments short and plain: one line max in most cases.

Never use an em dash (—) in comments. Use a plain ASCII hyphen (-) instead.

## Source Control Commits

Never add Claude (or any AI assistant) as a co-author in git commits or other source control metadata. Do not append `Co-Authored-By: Claude ...` or similar lines to commit messages, authors fields, or any other VCS metadata.

## ASCII Box Diagrams

Use Unicode box-drawing characters: `┌` `┐` `└` `┘` for corners, `─` for horizontal edges, `│` for vertical edges. Do not use `-`, `|`, or `+`.

Every box must close on all four sides with strict corner alignment:
- `┌` and `└` must be in the same column (left wall is vertical)
- `┐` and `┘` must be in the same column (right wall is vertical)
- `┌` and `┐` must be on the same line (top edge is horizontal)
- `└` and `┘` must be on the same line (bottom edge is horizontal)

Walls must be continuous — every cell between two corners on the same edge must be filled with `─` or `│`. The only permitted breaks are for arrows crossing the
wall or inline text labels. A gap or space in a wall is wrong.

Label outer container boxes in the top edge: `┌─ AWS ─────┐`. Indent contents 2 spaces from the `│` wall. Fix outer wall column positions first, then place inner
elements relative to those columns.

## Vendor and Provider Neutrality

Never suggest, prefer, or default to a specific vendor, service, or provider — for AI models, APIs, cloud platforms, databases, or any other external dependency — unless the user has already made that choice or the project is explicitly locked to one. Always leave the choice to the user. Do not name classes, variables, files, or modules after a specific vendor or product (e.g. avoid `ClaudeClient`, `StripeHelper`, `aws_service.py`) unless the code is specifically and exclusively for that provider and the user has named it that way themselves. Use generic, capability-describing names (e.g. `LLMClient`, `payment_service.py`, `storage_backend`) by default.

## Tools

Use modern/fast tools that conserve token usage.

Prefer `rg` (ripgrep) over `grep -r` and `fd` over `find` for searching the codebase. They are faster, respect `.gitignore` by default, and produce less noisy output — which also reduces token
  usage from large irrelevant result sets.

- Use `rg -l` when only the matching filenames are needed, not the lines themselves.
- Use `git diff --stat` when only the list of changed files is needed, not the full diff.
- Use `rg --type py` / `rg --type ts` etc. instead of `--include` glob patterns.
- Use `ast-grep` instead of `sed` or manual edits when mass updating files.

Use tools that are new standards and work faster/better than what they're replacing.

Prefer `uvx` over `pip`, for one off jobs.
