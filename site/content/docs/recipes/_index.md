---
title: "Workflow Recipes"
description: "Practical, prompt-driven playbooks for running parallel coding agents, cross-provider reviews, remote devboxes behind NAT, event-driven CLI scripts, and migrating from tmux, Orca, or Conductor."
type: docs
weight: 2
sidebar:
  open: true
---

These recipes combine LeapMux worktrees, remote Workers, Agent Skills, and the `leapmux` command-line interface (CLI) to solve real engineering workflows. Each playbook includes both a natural-language prompt for your coding agent and the underlying CLI commands.

{{< cards >}}
  {{< card link="/docs/recipes/parallel-agents/" title="Run Parallel Coding Agents" icon="view-grid" subtitle="Fan out a task across three agents in isolated git worktrees, inspect their diffs, and merge the winner." >}}
  {{< card link="/docs/recipes/second-opinion-review/" title="Get a Second-Opinion Review" icon="shield-check" subtitle="Spawn a read-only reviewer from a contrasting provider family with JSON-schema-validated findings." >}}
  {{< card link="/docs/recipes/remote-devbox/" title="Connect a Remote Devbox Behind NAT" icon="server" subtitle="Attach a Linux or GPU workstation to your Hub with zero open inbound ports and forward local services." >}}
  {{< card link="/docs/recipes/cli-automation/" title="Automate Agents & Terminals Without Polling" icon="terminal" subtitle="Script agents, shells, and event streams with --wait, --output-schema, --change, and checkpoints." >}}
  {{< card link="/docs/recipes/migrating/" title="Migrating from tmux, Orca, or Conductor" icon="sparkles" subtitle="Map your existing habits to LeapMux workspaces, worktrees, terminals, and remote Workers." >}}
{{< /cards >}}
