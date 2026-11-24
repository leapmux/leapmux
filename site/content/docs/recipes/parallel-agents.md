---
title: "Run Parallel Coding Agents"
description: "Run three coding agents in isolated git worktrees, wait without polling, inspect their diffs, and merge the winning result."
type: docs
weight: 1
---

When a feature splits into independent slices — or when you want three providers to attempt the same tricky refactor in parallel — git worktrees keep every agent from stepping on the others.

## Prerequisites

- LeapMux running in solo or distributed mode with at least one repository on your Worker.
- Optional: install the bundled `leapmux-delegate` skill so your coordinator agent knows this playbook natively:

```sh
leapmux skill install leapmux leapmux-delegate
```

## Prompt your coordinator agent

If you installed `leapmux-delegate`, open one coordinator agent in your repository and invoke `/leapmux-delegate`:

```text
/leapmux-delegate Split this task across three parallel agents in isolated worktrees off main:
1. backend-api: implement the rate-limiter middleware under backend/internal/api/ and add unit tests.
2. cli-flags: add the --rate-limit flag under backend/cmd/leapmux/ with tests.
3. docs-update: document the rate-limit configuration in site/content/docs/admin/configuration.md.
Wait without polling, inspect each diff, verify tests pass, and summarize what is ready to merge.
```

{{< screenshot src="coding-agent-chat" alt="LeapMux coding agent chat streaming tool calls, inline diffs, and composer status bar chips" caption="Each parallel agent streams tool calls, diffs, and permission prompts in its own tab and worktree." width="623" height="860" >}}

## Run the same workflow from the CLI

You can also drive all three agents directly from a shell or script. Write each assignment to a file so multiline briefs never break shell quoting, then launch three isolated worktrees:

```sh
A1=$(leapmux agent open --provider claude --worktree feat/backend-api --base main \
  --name backend-api --no-focus --file .tmp/brief-api.md -q)
A2=$(leapmux agent open --provider codex --worktree feat/cli-flags --base main \
  --name cli-flags --no-focus --file .tmp/brief-cli.md -q)
A3=$(leapmux agent open --provider gemini --worktree feat/docs-update --base main \
  --name docs-update --no-focus --file .tmp/brief-docs.md -q)
```

Block until the first agent finishes or asks for permission — no sleep loop required:

```sh
leapmux agent wait "$A1" "$A2" "$A3" --any --timeout 20m
```

Check whether any agent stopped at a permission prompt (`waiting_for_user`):

```sh
leapmux agent requests
leapmux agent approve "$A1"
```

## Inspect diffs and merge the winner

Never merge based on an agent's summary alone. Inspect each worktree's status and diff from the right-hand **Files** sidebar or from the CLI:

```sh
leapmux worktree list
leapmux git diff --base main --dir /path/to/worktree-backend-api
leapmux worktree inspect /path/to/worktree-backend-api
```

Merge or cherry-pick the verified commits into your target branch and run the full test suite yourself. Once integrated, close the finished agent tabs and explicitly choose what happens to each worktree:

```sh
# Discard temporary worktrees whose commits are already merged (or rejected attempts)
leapmux agent close "$A1" "$A2" "$A3" --worktree discard
```

Pass `--worktree keep` instead if you want to leave a branch and directory on disk for further edits.

## Next steps

- [Agent Skills](/docs/using/agent-skills/) — install all six bundled multi-agent playbooks.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — learn how dirty-worktree protection works on close.
- [Get a Second-Opinion Review](/docs/recipes/second-opinion-review/) — audit the merged diff with a fresh provider.
