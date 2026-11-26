---
title: "Control Command Reference"
description: "Every control command of leapmux with its arguments, flags, and examples, generated from the command tree of the binary."
type: docs
weight: 2
sidebar:
  open: false
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

This section lists every control command of `leapmux` with its arguments, flags, and examples, generated directly from the binary. The binary prints the same help with `--help`, and `leapmux commands <prefix> --json` prints it for a program. See [CLI Guide](/docs/using/cli-guide/) for targeting rules, output formats, exit codes, and authentication.

## Common tasks

| Task | Command |
|---|---|
| Open an agent in a new worktree | `leapmux agent open --provider claude --worktree feat/name --base main --dir .` |
| Send a prompt and wait for the reply | `leapmux agent send <agent> "..." --wait` |
| Request structured JSON from an agent | `leapmux agent send <agent> "..." --wait --output-schema schema.json` |
| Wait for any of several agents to finish | `leapmux agent wait <a1> <a2> --any` |
| Answer a pending permission or question prompt | `leapmux agent request answer <agent> <request-id> ...` |
| Run a command in a terminal and wait for exit | `leapmux terminal open --dir . --command "make test; exit" --wait --exit` |
| Inspect git status and diffs on a Worker | `leapmux git status --dir .` |
| Stream workspace and agent events with checkpoints | `leapmux events --checkpoint <name>` |
| Forward a TCP port to a remote Worker | `leapmux tunnel forward 8888:127.0.0.1:8888 --worker name:<worker>` |
| Rotate an OAuth/OIDC provider client secret | `leapmux admin idp rotate-secret <idp> --client-secret <secret>` |

## Flags of every command

Every control command takes these output flags, and every online command takes `--hub` (offline commands such as `guide`, `context show`, and `context clear` do not contact a Hub). Commands marked as not supporting `-q` / `--quiet` reject `--quiet` with exit code 2.

| Flag | Description |
|---|---|
| `--color string` | when to emit ANSI colors on TTY output (one of: auto, always, never; default: auto) |
| `--fields string` | comma-separated columns or fields to print |
| `--format string` | output format (one of: text, json, ndjson, tsv; default: text) |
| `--hub string` | hub URL (default: tab socket, else $LEAPMUX_HUB, else context/default credential, else local solo/dev Hub) |
| `--json` | print the result as one JSON envelope per line |
| `--no-header` | omit the header row from table and TSV output |
| `-q, --quiet` | print only the ids of the result, one per line |
| `-w, --wide` | do not truncate table columns |

## Command groups

{{< cards >}}
  {{< card link="/docs/reference/commands/agents/" title="Agents" icon="cube" subtitle="Start, message, queue, watch, and configure coding agents." >}}
  {{< card link="/docs/reference/commands/terminals/" title="Terminals" icon="terminal" subtitle="Open, type into, read, resize, and manage shell terminals." >}}
  {{< card link="/docs/reference/commands/workspaces-and-tabs/" title="Workspaces & Tabs" icon="collection" subtitle="Manage workspaces, sidebar sections, tabs, and the tree overview." >}}
  {{< card link="/docs/reference/commands/layout-and-windows/" title="Layout & Windows" icon="view-grid" subtitle="Split tiles, arrange layouts, and manage floating windows." >}}
  {{< card link="/docs/reference/commands/git-and-files/" title="Git & Files" icon="code" subtitle="Inspect git state, manage worktrees, and browse or search files on a Worker." >}}
  {{< card link="/docs/reference/commands/workers-and-events/" title="Workers & Events" icon="chip" subtitle="Manage Workers, TCP/stdio tunnels, live event streams, and notifications." >}}
  {{< card link="/docs/reference/commands/auth-and-session/" title="Auth & Session" icon="key" subtitle="Sign in, set CLI context and preferences, run diagnostics, inspect skills, and call RPCs." >}}
  {{< card link="/docs/reference/commands/admin/" title="Admin" icon="shield-check" subtitle="Administer Hub users, sessions, settings, OAuth apps, and Workers." >}}
{{< /cards >}}
