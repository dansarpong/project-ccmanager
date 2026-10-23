# Contributing to ccmanager

Thanks for your interest in improving ccmanager. Bug reports, fixes, and
well-scoped features are all welcome. This guide covers how to get set up and
what a change needs before it can be merged.

By participating you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
To report a security issue, **do not open a public issue** — follow
[SECURITY.md](SECURITY.md) instead.

## Getting set up

You need **Go 1.26+** on your `PATH`. The `claude` CLI is optional; without it
the LLM advisor shows an "unavailable" banner and everything else still works.

```sh
git clone https://github.com/dansarpong/ccmanager.git
cd ccmanager

go build ./...
go run . -port 8765          # then open http://127.0.0.1:8765
go test ./...                # all tests
go test ./internal/memory/   # one package
go test ./internal/memory/ -run TestStripBlockComments   # one test
```

To develop against a throwaway config instead of your real `~/.claude`, point
`CLAUDE_CONFIG_DIR` at a copy:

```sh
CLAUDE_CONFIG_DIR=/path/to/a/copy go run .
```

The [README](README.md) explains the architecture: the package layout, the
memory load-replay engine, the advisor, and the on-disk format assumptions.

## Ground rules

- **Pure Go, single binary.** No CGO, and no JavaScript build chain — htmx is
  vendored under `internal/server/static/` and templates are embedded with
  `go:embed`.
- **Dependencies.** Open an issue before adding one. The bar is high.
- **Formatting.** Code must be `gofmt`-clean and pass `go vet ./...`. CI checks
  both.

## Safety invariants

ccmanager edits memory files and deletes sessions, so its safety model is the
point of the tool. A change that weakens any of these will not be merged:

- **Atomic writes** — every write goes through `internal/safeio` (temp file →
  fsync → rename). A reader must never see a half-written file.
- **Backup before overwrite** — saving an edited memory file first copies the
  on-disk version into the trash.
- **Deletes are recoverable** — deletes move files to
  `<config-dir>/ccmanager/trash/` with a manifest entry and a 30-day restore
  window. A session's sidecar folder moves with its transcript.
- **Change detection** — a save warns on conflict if the file changed on disk
  since it was opened.
- **Edits stay in scope** — only `CLAUDE.md`, `CLAUDE.local.md` and rules files
  are editable (`internal/server/scope.go`). Auto memory is view-and-delete,
  managed policy is view-only, and any other path gets a 403.
- **App data stays private** — `<config-dir>/ccmanager/` is `0700` and the index
  `0600`. Moving, copying or restoring a file must never loosen its mode.
- **Localhost only** — the server binds `127.0.0.1` and nothing else.
- **Browser requests are checked** — `Server.Routes` rejects any Host other
  than `127.0.0.1` or `localhost`, and cross-origin requests that aren't GET,
  HEAD, or OPTIONS. **GET handlers must never change state**, because the
  cross-origin check doesn't cover them.
- **The advisor never writes on its own** — it runs `claude` with no tools, no
  MCP servers, no hooks and no saved session, from an empty working directory,
  and only proposes diffs the user approves.

## Tests

- **Memory engine** (`internal/memory`): `memory.Resolve` is a pure function
  tested against fixture trees under `internal/memory/testdata/`. Behaviour
  changes come with a fixture that exercises them.
- **JSONL parser** (`internal/sessions`): add cases to
  `internal/sessions/parse_test.go` for any new or changed transcript shape.
- **On-disk format.** Claude Code's transcript layout is not a stable contract.
  If your change relies on a new format assumption, say in the PR which
  `claude --version` you verified it against.

## Pull requests

1. Fork the repo and create a branch from `main`.
2. Keep each PR to one focused change; split unrelated fixes.
3. Make sure `gofmt -l .` prints nothing and `go vet ./...` and `go test ./...`
   pass.
4. Update the README if you change user-visible behaviour.
5. Open the PR and fill in the template.

Commit messages and PR titles follow
[Conventional Commits](https://www.conventionalcommits.org/): a single
`type(scope): summary` line — imperative, lowercase, no trailing period.
Common types are `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, and `ci`;
the scope is usually a package name. For example:

```
feat(memory): show conditional rules in the resolved view
fix(sessions): tolerate a missing cwd field
docs: clarify the trash restore window
```

## Scope

The README lists the v1 non-goals (desktop-app and VS Code session stores, a
filesystem watcher, usage analytics, multi-machine sync, a packaged desktop
app). Please open an issue to discuss anything in that list before starting
work on it.

## License

ccmanager is released under the [MIT License](LICENSE). By contributing, you
agree that your contributions are licensed under the same terms.
