---
title: "Supported Agents"
description: "E2E-verified capability matrix for all LeapMux coding-agent providers, generated directly from automated test specifications."
type: docs
weight: 3
---

LeapMux integrates {{< agent-provider-count >}} coding-agent providers. Every provider supports interactive chat and streamed tool calls inside LeapMux.

{{< agent-logos >}}

For day-to-day usage — opening agents, input queueing, steering, permission prompts, mid-session settings, forking, and resuming sessions — see [Coding Agents](/docs/using/coding-agents/). If an installed provider does not appear in the picker, see [Agents & Terminals Troubleshooting](/docs/reference/troubleshooting/agents-and-terminals/).

## How this matrix is built

This matrix is generated directly from LeapMux's automated end-to-end (E2E) capability specifications, so every cell reflects tested behavior rather than manual claims.

- **Legend:** The legend above the first table explains the three support symbols shown in each cell.
- **Layout:** Providers are split across multiple tables, with features grouped by area. Each group header after the first repeats the provider logos so you can keep your place while scrolling.
- **Definitions and notes:** Select any feature label to jump to its exact definition below the tables. Select a `†` link beside a provider or cell to open a short note describing provider-specific behavior or limits.

## Feature matrix

{{< matrix >}}

## Detected CLI executables

Each Worker probes `PATH` (and login-shell profiles when `--use-login-shell` is enabled) for these executable names. If a provider does not appear in the picker, confirm that the listed command resolves on that Worker.

| Agent | Command probed on Worker |
| --- | --- |
| Claude Code | `claude` |
| Codex | `codex` |
| Cursor | `cursor-agent` |
| GitHub Copilot | `copilot` |
| Kilo | `kilo` |
| OpenCode | `opencode` |
| Goose | `goose` |
| Pi | `pi` |
| Reasonix | `reasonix` |
| ZCode | `zcode` on `PATH`, or its desktop application |
| Codewhale | `codewhale` |
| Kimi Code | `kimi` |
| MiMo Code | `mimo` |
| Qwen Code | `qwen` |
| Oh My Pi | `omp` |
| Grok Build | `grok` |
| Kiro | `kiro-cli-chat` |
| Amp | `amp` |
| Cline | `cline` |
| CodeBuddy Code | `codebuddy` or `cbc` |
| Junie | `junie` |
| Letta Code | `letta` |
| Dirac | `dirac` |
| Qoder CLI | `qodercli` |
| Factory Droid | `droid` |
| Fast Agent | `fast-agent` |
| Command Code | `command-code`, `commandcode`, or `cmdc` |
| DeepSeek Harness | `dsh` |
| Gemini CLI | `gemini` |
| Muse Code | `muse` |
| Antigravity CLI | `agy` |
