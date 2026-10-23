# Security Policy

ccmanager runs a local web server that can edit your Claude Code memory files
and delete session transcripts, and those transcripts can contain private data.
Security reports are taken seriously.

## Supported versions

There are no tagged releases yet. Fixes land on `main` only, so please check
that an issue reproduces on the latest `main` before reporting.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it privately through GitHub:
<https://github.com/dansarpong/ccmanager/security/advisories/new>

Include as much of the following as you can:

- what an attacker can do, and what they need first (for example, "the victim
  visits a web page while ccmanager is running")
- steps to reproduce or a proof of concept
- the ccmanager commit (`git rev-parse --short HEAD`), your OS, and
  `claude --version` if it's relevant

Leave real transcript or memory contents out of the report — use a minimal
example instead.

ccmanager is maintained in spare time, so responses are best-effort. You will
get an acknowledgement, and fixes will be credited in the advisory unless you'd
rather stay anonymous.

## Scope

In scope:

- Anything that lets the server be reached or driven by someone other than the
  local user. That includes requests from another machine, and cross-origin or
  DNS-rebinding requests sent by a web page open in the user's browser, and any
  bypass of the Host or cross-origin checks.
- Edit, delete, or restore routes that read or write outside the intended
  config and project directories (path traversal).
- Write or delete paths that bypass the atomic write, the backup-before-overwrite,
  or the trash.
- The advisor gaining tool or filesystem access when it runs `claude`.
- Files ccmanager writes, moves or indexes becoming readable by other local
  users.
- Cross-site scripting through rendered transcripts, memory files, or advisor
  output.

Out of scope:

- Attacks that require already running code as the local user, or already
  having read access to `~/.claude`.
- Vulnerabilities in the `claude` CLI itself — report those to Anthropic.
- Vulnerabilities in upstream dependencies, unless ccmanager uses them in a way
  that creates the issue.
