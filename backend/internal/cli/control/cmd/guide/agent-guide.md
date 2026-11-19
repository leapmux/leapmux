# Driving LeapMux with `leapmux control`

This guide is for an agent that runs inside a LeapMux tab and controls other
tabs: it starts agents and terminals, sends them work, reads their output, and
closes them. Every command prints its own help with `--help`, and
`leapmux commands --json` lists every command with its arguments and flags.

`leapmux` on your `PATH` is the CLI of the Worker that started you, so it
matches the Worker's version. A shell profile can move another `leapmux`
ahead of it; `"$LEAPMUX_CONTROL_CLI"` always runs the Worker's own.

## Rules that keep you safe

- A command that changes or removes an object needs that object as its first
  argument. Nothing changes your own tab unless you write `self`.
- `tab close`, `tile close` and `workspace delete` refuse to remove the tab
  that runs the command. `--allow-self` overrides that.
- `tab close` refuses to close a tab whose agent works or whose terminal runs a
  process. `--allow-busy` overrides that.
- Write a text that starts with `-` after `--`, so that a misspelled flag never
  reaches an agent as text: `leapmux control agent send 87a19d5f -- "-v"`.
- Use full command names in scripts. A short form such as `leapmux control ag`
  works today, and a later release can make it ambiguous.

## How a command finds its object

An argument such as `<agent>`, `<tab>` or `<workspace>` takes one of these:

- A full id.
- A unique prefix of an id, at least 4 characters long. A table prints the
  first 8 characters of each id, and they work as a prefix.
- `title:<exact title>` for a tab or a workspace.
- `name:<name>` for a worker.
- `self` for the tab, tile, workspace or worker that runs the command.

A command that only reads defaults to `self`: `leapmux control agent get` shows
your own agent. A prefix that matches more than one object fails with the code
`ambiguous_target` and lists the candidates.

## Output

- Text is the default: a table for a list, `key: value` lines for one object.
- `--json` prints one envelope per line: `{"data": ...}` for a result and
  `{"error": {"code": ..., "message": ..., "hints": [...]}}` for a failure.
- `-q` prints only ids, one per line: `A=$(leapmux control agent open -q)`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | The hub, the worker or the agent failed |
| 2 | Usage error; nothing was sent |
| 3 | Not logged in, or permission denied |
| 4 | The target does not exist, or more than one object matches it |
| 5 | A guard refused; the hint gives the flag that overrides it |
| 6 | A wait ended before its condition held |
| 7 | Unavailable now; a retry can succeed |

## Common tasks

Start an agent beside you, give it work, and read what it did:

```sh
A=$(leapmux control agent open --provider claude -q)
leapmux control agent send "$A" "Run the tests and fix what fails"
leapmux control agent get "$A"            # state: idle, working or waiting_for_user
leapmux control agent messages "$A"
```

Watch what happens in your workspace, one event per line:

```sh
leapmux control events --workspace self --kind agent --json
```

Start an agent in its own git worktree:

```sh
leapmux control agent open --worktree fix-flaky-spec --base main --message "Fix the flaky spec"
```

Run a command in a new terminal and read its screen:

```sh
T=$(leapmux control terminal open -q)
leapmux control terminal run "$T" 'make test'
leapmux control terminal read "$T" --lines 40
```

See what runs where:

```sh
leapmux control tab list
leapmux control agent list --workspace self
leapmux control worker list
```

Close a tab you started when its work is done:

```sh
leapmux control tab close "$A"
```
