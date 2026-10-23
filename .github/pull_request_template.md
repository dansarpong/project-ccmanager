## Summary

<!-- What does this change and why? Link the issue it addresses, if any. -->

## Testing

<!-- How you verified it: tests added or run, and manual steps in the UI. -->

## Checklist

- [ ] The PR title follows [Conventional Commits](https://www.conventionalcommits.org/) (`type(scope): summary`)
- [ ] `gofmt -l .` prints nothing, and `go vet ./...` and `go test ./...` pass
- [ ] The [safety invariants](https://github.com/dansarpong/ccmanager/blob/main/CONTRIBUTING.md#safety-invariants) still hold (atomic writes, backup before overwrite, recoverable deletes, change detection, scoped edits, private app data, `127.0.0.1` only)
- [ ] Changes to the memory engine or the JSONL parser come with tests
- [ ] If this relies on a new on-disk format assumption, I've noted the `claude --version` I verified it against
- [ ] The README is updated if user-visible behaviour changed
