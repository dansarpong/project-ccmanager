# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

ccmanager is a single static Go binary that serves a **localhost-only** server-rendered web UI for browsing, searching, and safely cleaning up the data Claude Code keeps under `~/.claude` — session transcripts (JSONL) and memory files (`CLAUDE.md`, rules, auto memory). It binds `127.0.0.1` only by design: it edits memory and deletes sessions, so it must never be reachable from another machine.

No database server, no JavaScript build chain (htmx is vendored as a static asset), no CGO. Full-text search is pure-Go via `modernc.org/sqlite` (ships FTS5 with no build tag).

## Build, run, test

Go 1.26+ is required.

```sh
go build ./...
go run . -port 8765          # default port 8765; always bound to 127.0.0.1
go test ./...                # all tests
go test ./internal/memory/   # one package
go test ./internal/memory/ -run TestStripBlockComments   # one test
```

If `go mod tidy` or the first build fails with a proxy connection reset, retry or set `GOSUMDB=off`.

### Configuration
- `CLAUDE_CONFIG_DIR` (env) — which Claude Code config dir to read; default `~/.claude`.
- `-port` (flag) — listen port; always `127.0.0.1`.
- `claude` CLI on `PATH` — optional; the advisor degrades gracefully if absent.

ccmanager keeps its own state under `<config-dir>/ccmanager/` (`0700`): the SQLite index `index.db` (`0600`), `trash/`, and `advisor/` (the advisor's empty working directory). It never writes into Claude Code's data except when you explicitly edit or delete a file.

## Architecture

Entry point `main.go` wires everything: resolve config → open store → purge expired trash → index transcripts → start the HTTP server. Indexing is incremental (only changed files are re-scanned).

Packages under `internal/` (each is a focused subsystem; the layering matters more than any single file):

- **`config`** — resolves the config root, app paths, the `claude` binary, and port; creates the private data dirs.
- **`store`** — SQLite: schema, migrations, queries, FTS5; also backs trash and the advisor cache. Tables: `sessions` (metadata per transcript), `messages` + `messages_fts` (text bodies + full-text index), `trash` (deletion manifest), `advisor_cache` (LLM results keyed by content hash). Only metadata and the FTS index are stored; transcript bodies stream from disk on demand.
- **`sessions`** — defensive JSONL parsing (`parse.go`), scan/title extraction (`scan.go`, `summary.go`), the indexer, and transcript paging.
- **`memory`** — the pure load-replay engine (see below).
- **`advisor`** — static lint + the `claude` print-mode client + diff generation.
- **`trash`** — move-to-trash (rename, copy fallback only across devices), backup-before-write, restore, roll back, 30-day purge.
- **`safeio`** — atomic write (temp + fsync + rename, through symlinks) and on-disk change detection.
- **`server`** — HTTP handlers, htmx routes, `html/template` rendering. Templates (`templates/*.html`) and static assets (`static/*`, including vendored `htmx.min.js`) are **embedded via `go:embed` in `internal/server/render.go`**. `scope.go` decides what a memory route may touch; `errors.go` (`friendlyErrors`) turns plain-text errors into pages or htmx flash messages.

### The memory load-replay engine (`internal/memory`)

`memory.Resolve` is a **pure** function that reproduces how Claude Code assembles memory for a working directory (filesystem in → ordered structure with provenance out). Keep it pure — that is what makes it testable against fixture trees; machine facts (managed paths, home dir, GOOS) come in through `memory.Input`. It mirrors the documented resolution (https://code.claude.com/docs/en/memory):

1. Collect sources broadest-scope-first (managed policy → user `CLAUDE.md` + user rules → walk root-down to cwd collecting `CLAUDE.md`, `CLAUDE.local.md`, `.claude/CLAUDE.md`, `.claude/rules/**/*.md`). A file on both user and project layers loads once, at its broadest position. `memory.Candidates` is this list, stat-only; the inventory reuses it, so `Resolve` and the inventory can't drift.
2. Expand `@path` imports relative to the importing file, recurse **≤4 hops**, stop on cycles, skip imports inside code spans/fences, mark imports resolving outside the project as external (approval-gated).
3. Mark conditional rules (`paths:` frontmatter) and `claudeMdExcludes` globs, merged from managed, user, `<cwd>/.claude/settings.json`, and `.claude/settings.local.json` at the repo root (plus the legacy cwd copy). The managed `CLAUDE.md` is never excluded.
4. Append auto memory: only the first **200 lines or 25 KB** of `MEMORY.md` loads at session start; topic files are on-demand. The auto-memory dir is keyed by the git repo — a worktree's `.git` file resolves to the main checkout — and `projects/` folder names map every non-alphanumeric rune to `-` (names over 200 chars carry an undocumented hash, so they're matched by prefix). `autoMemoryDirectory` overrides it, highest layer wins.
5. Produce two renderings: "what Claude sees" (imports inlined, block-level HTML comments stripped but preserved inside code fences) and "raw" (file as on disk).

Managed policy lives in `memory.ManagedDir(goos)`: `/etc/claude-code`, `/Library/Application Support/ClaudeCode`, `C:\Program Files\ClaudeCode`.

### The advisor (`internal/advisor`)

Two layers. **Static lint** is always on, offline, instant (flags files >200 lines, vague words, duplicate/near-duplicate lines, dead `@path` imports). **LLM analysis** is on-demand: it shells out to the local `claude` CLI in print mode (`cliArgs` in `claude.go`):

```
claude -p --output-format json --tools "" --allowed-tools "" --disallowed-tools "mcp__*" \
  --no-session-persistence --disable-slash-commands --settings '{"disableAllHooks":true}' --strict-mcp-config
```

It runs with `cmd.Dir` set to `<data-dir>/advisor`, because `-p` loads the hooks, `.mcp.json` and `CLAUDE.md` of its working directory. `--strict-mcp-config` is dropped when a `managed-mcp.json` is deployed — Claude Code exits at startup with both. Do **not** add `--bare` (it suppresses subscription credential loading, so a logged-in user gets "Not logged in") or `--setting-sources` (drops `apiKeyHelper`/`env` from user settings). Results are shown as diffs the user approves; applying one goes through the normal safe-write path. The advisor never writes on its own.

## Invariants — do not break these

The safety model is the point of the tool. When changing write/delete paths:

- **All writes are atomic** (`safeio`: temp file → fsync → rename). A reader must never see a half-written file.
- **Backup before overwrite** — saving an edited memory file first copies the on-disk version into trash. Rolling back to a backup backs up the version it replaces.
- **Deletes are recoverable** — move to `<config-dir>/ccmanager/trash/` with a manifest entry; 30-day restore window; purge runs opportunistically on launch and only removes direct children of the trash dir. A session's `<id>/` sidecar moves with its transcript.
- **Change detection** — record mtime+size when opening a file to edit; warn on conflict if it changed on disk before save.
- **Edits stay in scope** (`server/scope.go`) — only `CLAUDE.md`, `CLAUDE.local.md` and rules files are editable; auto memory is view and delete; managed policy is view only; everything else is 403.
- **App data stays private** — data dirs `0700`, index `0600`, restored transcripts `0600`; moving or copying a file must keep its mode.
- **Localhost only** — never bind anything other than `127.0.0.1`.
- **Browser requests are checked** — `Routes()` wraps the mux in `localOnly` (Host must be `127.0.0.1`/`localhost`; defeats DNS rebinding) and `http.CrossOriginProtection` (rejects cross-origin non-GET). GET handlers must never change state. Tests must set `req.Host`: `httptest.NewRequest` defaults to `example.com`, which is rejected.
- **Report errors as plain text** (`http.Error`, `s.fail`) and let `friendlyErrors` render them. htmx is configured to swap 4xx/5xx, so a handler that writes its own HTML error would land in the requesting element's target.

## On-disk format assumptions (verified against Claude Code 2.1.186–2.1.309)

The JSONL/transcript layout is not a stable contract; assumptions are isolated and confirmed at runtime where possible:

- Working directory comes from the `cwd` recorded **inside** the transcript, never from the `projects/<name>` folder name (`sessions/scan.go`).
- No `summary` line type and no `sessions-index.json` — the JSONL files are the source of truth. Display name = `custom-title` → `ai-title` → first non-meta user prompt (`sessions/summary.go`, `sessions/scan.go`).
- `message` and `message.content` are **polymorphic** (object or JSON string; string or list of typed blocks). The parser tolerates unknown/missing fields and a truncated final line (`sessions/parse.go`).
- A transcript `<id>.jsonl` has a sidecar folder `<id>/` (`subagents/`, `tool-results/`, `mcp-tasks/`) (`trash/trash.go`).
- The headless `claude` envelope is `{type:"result", is_error, result, ...}`; the advisor reads `.result` and treats `is_error` (e.g. "Not logged in") as unavailable (`advisor/claude.go`). With `--no-session-persistence` a run still creates an empty `projects/<cwd>/memory/` once.

## Tests

The two subtle subsystems are covered by focused tests. The **memory engine** uses table-driven tests against fixture trees under `internal/memory/testdata/` (nested `CLAUDE.md`, a `paths:`-frontmatter rule, a 4-hop import chain with a cut 5th hop, a cycle, an external import, a `@path` in a code block) plus temp-dir tests for settings layers, worktrees and folder-name encoding. The **JSONL parser** is tested for unknown/missing fields, a truncated final line, and the polymorphic shapes. Trash, safeio, config, store, scope, errors and advisor lint/diff/analyze/args also have tests.

Gotchas: the server package's `TestMain` points `managedPolicyDir` at an empty temp dir so a real `/etc/claude-code` never leaks into results; `html/template` percent-encodes query values with lowercase hex (`%2f`), so assert rendered `href`s accordingly.

## Out of scope for v1

Desktop-app/VS Code session stores (CLI store only), a filesystem watcher (refresh is manual), usage/cost analytics, multi-machine sync, a packaged desktop app, and session-keyed data outside `projects/` (`file-history/`, `session-env/`, …). Editing is limited to `CLAUDE.md`, `CLAUDE.local.md`, and rules files.
