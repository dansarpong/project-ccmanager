# ccmanager

[![CI](https://github.com/dansarpong/ccmanager/actions/workflows/ci.yml/badge.svg)](https://github.com/dansarpong/ccmanager/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A small, local web tool for seeing, searching, and safely cleaning up everything
Claude Code keeps on your machine: your **session transcripts** and your **memory
files** (`CLAUDE.md`, rules, and auto memory).

It is a single static Go binary that serves a server-rendered web UI on
`127.0.0.1` only. There is no database server, no JavaScript build chain, and no
network service exposed to your LAN — it edits memory and deletes sessions, so it
must never be reachable from another machine.

---

## Why this exists

Claude Code stores a lot under `~/.claude`: one JSONL transcript per session
(these add up to hundreds of megabytes), plus a growing pile of memory files
across every project. There is no built-in way to browse them, full-text search
across them, or safely prune them — and no way to *see* how memory actually gets
assembled for a given working directory. ccmanager fills those gaps:

- **Sessions** — browse every transcript, search across all of them, and delete
  the ones you no longer need (recoverably).
- **Memory** — see the exact, ordered stack of memory files Claude Code loads for
  a directory, with both a "what Claude sees" rendering and the raw files;
  inventory every memory file across your projects; edit the ones you own; and get
  lint + LLM suggestions for improving them.
- **Safety by construction** — deletes go to a 30-day trash, edits are atomic and
  backed up, auto memory is read-only, and the LLM advisor only ever *proposes*.

---

## Build and run

Go 1.26+ is required and must be on your `PATH`. To install the binary:

```sh
go install github.com/dansarpong/ccmanager@latest
ccmanager -port 8765     # default port is 8765
```

Or build from a clone:

```sh
git clone https://github.com/dansarpong/ccmanager.git
cd ccmanager
go build ./...
go run . -port 8765
```

Then open <http://127.0.0.1:8765>. The first launch indexes all transcripts
(this can take a few seconds for a large history); subsequent launches only
re-index files that changed.

If `go mod tidy` or the first build fails with a proxy connection reset, retry,
or set `GOSUMDB=off`.

### Configuration

| What | How | Default |
| --- | --- | --- |
| Which Claude Code config dir to read | `CLAUDE_CONFIG_DIR` env var | `~/.claude` |
| Listen port (always bound to `127.0.0.1`) | `-port` flag | `8765` |
| `claude` CLI for the advisor | found on `PATH` | optional; advisor degrades gracefully if absent |

ccmanager keeps its own state (the SQLite index, the trash and the advisor's
empty working directory) under `<config-dir>/ccmanager/`, so it never writes
into Claude Code's own data except when you explicitly edit or delete a file.

---

## Safety model

This tool can change and delete your files, so it is conservative by design:

- **Localhost only.** The server binds `127.0.0.1`. It is never reachable from
  the network.
- **Browser requests are checked.** Every request must be addressed to
  `127.0.0.1` or `localhost`, which defeats DNS rebinding, and any request that
  changes something must come from ccmanager's own pages (Go's
  `http.CrossOriginProtection`). Another website open in your browser can't
  trigger a delete or an edit.
- **Trash with 30-day restore.** Deleting a session or a memory file moves it
  into `<config-dir>/ccmanager/trash/` with a manifest entry; it is restorable
  for 30 days, then purged. A session's sidecar folder (`<id>/`, holding subagent
  transcripts and large tool results) moves with its transcript and comes back
  with it. Moves are renames, so permissions are kept. Purge runs
  opportunistically on launch, and there is an "Empty now" action.
- **Private by default.** `<config-dir>/ccmanager/` and its trash are `0700` and
  the index is `0600`: the index holds the text of every transcript, so it gets
  the owner-only access Claude Code gives the transcripts themselves.
- **Atomic writes.** Every write goes to a temp file in the same directory,
  is `fsync`'d, and renamed into place, so a reader never sees a half-written
  file. Writing a symlinked file (say `CLAUDE.md` → `AGENTS.md`) writes the
  target and leaves the link in place.
- **Backup before overwrite.** Saving an edited memory file first copies the
  current on-disk version into the trash. "Roll back" on that backup swaps it
  in, after backing up the version it replaces, so a roll back can be undone too.
- **Scoped edits.** Only `CLAUDE.md`, `CLAUDE.local.md` and rules files
  (`~/.claude/rules/` and any `.claude/rules/`) can be edited; those and auto
  memory can be deleted. Managed policy is view-only. Any other path gets a 403.
- **Change detection.** When you open a file to edit, ccmanager records its
  mtime+size. If the file changed on disk before you save (e.g. Claude Code
  edited it), you get a conflict warning and must confirm before overwriting.
- **Active-session guard.** Deleting your most-recently-active session shows a
  confirmation first.
- **Auto memory is read-only.** Claude Code writes auto memory during sessions,
  so ccmanager will *view and delete* those files but never edit them (the edit
  routes return 403).
- **The advisor never writes on its own.** It only proposes diffs; applying one
  goes through the same backup + atomic-write path as a manual edit.

---

## The memory load-replay engine

The heart of the Memory section is a **pure** function (`memory.Resolve`) that
reproduces how Claude Code assembles memory for a working directory:
filesystem in, an ordered structure with provenance out. Keeping it pure makes
it straightforward to test against fixture trees.

It mirrors Claude Code's documented resolution
(<https://code.claude.com/docs/en/memory>):

1. **Collect sources in load order**, broadest scope first so the most specific
   instruction is read last:
   - Managed policy, if present: `CLAUDE.md` in `/etc/claude-code` (Linux and
     WSL), `/Library/Application Support/ClaudeCode` (macOS) or
     `C:\Program Files\ClaudeCode` (Windows).
   - User: `~/.claude/CLAUDE.md`, then user rules `~/.claude/rules/**/*.md`.
   - Walk the filesystem from the root down to the working directory; at each
     directory collect `CLAUDE.md`, then `CLAUDE.local.md`, then
     `.claude/CLAUDE.md`, then `.claude/rules/**/*.md`. Directories closer to the
     working dir are read after higher ones; within a directory `CLAUDE.local.md`
     comes after `CLAUDE.md`; user rules come before project rules.
   - A file that sits on both the user and project layers (e.g.
     `~/.claude/CLAUDE.md` when the project is under your home dir) is loaded
     once, at its broadest position.
2. **Expand `@path` imports.** Paths resolve relative to the importing file,
   recurse up to **4 hops**, stop on circular imports, skip imports inside
   Markdown code spans and fenced code blocks (a backticked `` `@path` `` is
   literal), and mark imports that resolve outside the project as *external
   (approval-gated)*.
3. **Mark conditional rules.** A rule with `paths:` frontmatter only applies to
   matching files, so it is flagged conditional rather than always-on.
   `claudeMdExcludes` glob patterns flag excluded files. They merge across
   managed settings (`managed-settings.json` beside the managed `CLAUDE.md`),
   user settings, the working directory's `.claude/settings.json`, and
   `.claude/settings.local.json` at the repository root (plus a legacy copy in the
   working directory). The managed `CLAUDE.md` can't be excluded.
4. **Append auto memory.** Only the first **200 lines or 25 KB** (whichever comes
   first) of `MEMORY.md` loads at session start; topic files are listed as
   on-demand. The auto-memory directory is keyed by the git repository, so a
   worktree shares its main checkout's. `autoMemoryDirectory` overrides it from
   any settings layer (managed, then local, project, user). Folder names under
   `projects/` replace every non-alphanumeric character with `-`.
5. **Produce two renderings.** "What Claude sees" inlines imports and strips
   block-level HTML comments (but preserves comments inside code fences, matching
   Claude Code); "Raw" is the file exactly as on disk.

Two UI surfaces use the engine: the per-project **resolved view** (pick a
directory, see the ordered stack with provenance, both renderings, and the import
graph) and the **global inventory** (managed policy, your user memory, every
memory file the engine would load in each project your sessions ran in, and
their auto memory, for cleanup across projects).

---

## Package layout and data model

```
ccmanager/
├── main.go                 flags, config, launch-time purge + index, start server
├── internal/
│   ├── config/             resolve config root, app paths, claude binary, port
│   ├── store/              SQLite: schema, migrations, queries, FTS5, trash + advisor cache
│   ├── sessions/           defensive JSONL parse, scan, title/summary, indexer, transcript paging
│   ├── memory/             the pure load-replay engine: resolve, import expansion, comment strip, auto memory
│   ├── advisor/            static lint + claude print-mode client + diff generation
│   ├── trash/              move-to-trash, backup-before-write, restore, 30-day purge
│   ├── safeio/             atomic write, on-disk change detection
│   └── server/             http handlers, htmx routes, html/template rendering;
│                           templates and static assets are embedded here
```

The SQLite index (`<config-dir>/ccmanager/index.db`) holds:

- **`sessions`** — one row per transcript: id, file path, project path (the `cwd`
  recorded *inside* the transcript), display name, titles, message/file size,
  last activity, and the first-message parent uuid for the resume/branch chain.
- **`messages`** + **`messages_fts`** — one row per text-bearing message and an
  FTS5 full-text index of their bodies, for cross-transcript search.
- **`trash`** — the deletion manifest (kind, original path, trash path, deleted-at,
  purge-after).
- **`advisor_cache`** — cached LLM results keyed by a hash of the analyzed text,
  so identical content isn't re-sent to `claude`.

Sessions and message bodies are streamed from disk on demand for the transcript
viewer; only metadata and the FTS index are stored, so the index stays small even
with a large transcript history.

---

## The advisor

The advisor has two layers.

**Static lint** is always on, fully offline, and instant. It flags memory files
over 200 lines, vague instructions (a small wordlist like "properly", "clean",
"appropriate", "nicely"), duplicate/near-duplicate lines across files
(contradiction candidates), and dead `@path` imports.

**LLM analysis** is on demand. It shells out to your locally installed `claude`
CLI in print mode, granting it **no tools**, so it can only analyze the text
passed on stdin and cannot touch your filesystem:

```
claude -p --output-format json --tools "" --allowed-tools "" \
  --disallowed-tools "mcp__*" --no-session-persistence --disable-slash-commands \
  --settings '{"disableAllHooks":true}' --strict-mcp-config
```

Built-in and MCP tools are off, no MCP servers start, hooks don't run, and the
run isn't saved as a session. It runs in an empty directory under
`<config-dir>/ccmanager/advisor/`, so the folder you launched ccmanager from
doesn't lend it its hooks, `.mcp.json` or `CLAUDE.md`. `--strict-mcp-config` is
left out when a managed `managed-mcp.json` is deployed, because Claude Code
refuses to start with both.

It offers two analyses: rewrites of weak instructions, and semantic contradiction
detection across the resolved stack. Each result is shown as a **diff you
approve**; applying one goes through the normal safe-write path (backup + atomic
write), and the advisor never writes on its own.

If `claude` is missing, errors, or is not logged in, the advisor shows a clear
banner and the static lint keeps working regardless.

> Note: `--bare` is deliberately **not** passed. In Claude Code 2.1.187 that flag
> suppresses subscription credential loading, so a logged-in user would still get
> "Not logged in". `--setting-sources` is left out for the same reason: it would
> drop `apiKeyHelper` and `env` from your user settings. The sandbox comes from
> the flags above, not from `--bare`.

---

## Assumptions discovered about the on-disk format

The JSONL/transcript layout is not a stable contract and shifts between Claude
Code versions, so ccmanager confirms what it can at runtime and isolates its
assumptions in a few places. These were verified against **Claude Code 2.1.186/187**:

- **The working directory comes from the `cwd` recorded inside the transcript**,
  never from decoding the `projects/<name>` folder name. (`sessions/scan.go`)
- **There is no `summary` line type** in this version. A session's display name
  comes from a `custom-title` envelope, then an `ai-title` envelope, then the
  first non-meta user prompt. (`sessions/summary.go`, `sessions/scan.go`)
- **`message` and `message.content` are polymorphic** — `message` may be an
  object or a JSON string; `content` may be a string or a list of typed blocks.
  The parser handles all of these and tolerates unknown/missing fields and a
  truncated final line. (`sessions/parse.go`)
- **No `sessions-index.json` exists**; the JSONL files are the source of truth.
- **A transcript has a sidecar folder** `projects/<project>/<id>/` (subagent
  transcripts, `tool-results/`, `mcp-tasks/`), so deleting a session moves both.
  (`trash/trash.go`)
- **The headless `claude` envelope** is `{type:"result", is_error, result, ...}`;
  the advisor reads `.result` and treats `is_error` (e.g. "Not logged in") as
  unavailable. (`advisor/claude.go`)
- **`modernc.org/sqlite` ships FTS5** with no build tag and no CGO, which is what
  makes a pure-Go single binary with full-text search possible. (`store`)

---

## Tests

The two subtle subsystems are covered by focused tests:

- The **memory engine** has table-driven tests against fixture trees under
  `internal/memory/testdata/`: nested `CLAUDE.md` files, a `.claude/rules/` file
  with `paths:` frontmatter, a 4-hop import chain (with a 5th hop that must be
  cut), a circular import, an external import, and a `@path` inside a code block.
- The **JSONL parser** is tested for unknown fields, a missing field, a truncated
  final line, and the polymorphic `message`/`content` shapes.

Run them with:

```sh
go test ./...
```

---

## v1 limitations and non-goals

The following are intentionally out of scope for v1; the code is structured so
they can be added later, but they are not implemented:

- Desktop-app and VS Code session stores (v1 reads the **CLI store only**).
- A filesystem watcher — refresh is manual.
- Usage/cost analytics.
- Multi-machine sync.
- A packaged desktop app.

Editing is limited to `CLAUDE.md`, `CLAUDE.local.md`, and rules files; auto memory
is view-and-delete only.

---

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, the
safety invariants every change must keep, and the PR process. Everyone taking
part is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md). Please
report security problems privately as described in [SECURITY.md](SECURITY.md).

---

## License

ccmanager is released under the [MIT License](LICENSE).

It bundles [htmx](https://htmx.org) 2.0.4
(`internal/server/static/htmx.min.js`), which is licensed under the Zero-Clause
BSD license. Go dependencies listed in `go.mod` are under their own licenses.
