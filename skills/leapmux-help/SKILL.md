---
name: leapmux-help
description: Answer questions about installing, configuring, using, or troubleshooting LeapMux (Hub, Workers, E2EE, tunnels, agent providers, workspaces, CLI, and settings) by consulting live LeapMux Markdown docs (`https://leapmux.dev/llms.txt`) and running minimal read-only diagnostics. Do not activate when the user asks you to drive LeapMux tabs or spawn agents (use `leapmux`, `leapmux-delegate`, `leapmux-review`, `leapmux-council`, or `leapmux-handoff`).
user-invocable: true
argument-hint: "<question, setup goal, or troubleshooting issue>"
---

# LeapMux Help & Troubleshooting

Help the user understand, configure, or troubleshoot LeapMux using authoritative live documentation and read-only diagnostics.

Load [references/diagnostics-checklist.md](references/diagnostics-checklist.md) (or run `leapmux skill show leapmux-help --reference diagnostics-checklist`) for the symptom-to-command matrix and bug-report template.

## 1. Ground Answers in Live Docs First

Never answer LeapMux feature, config, or CLI questions from stale memory:

1. Fetch `https://leapmux.dev/llms.txt` to discover the current documentation index and per-page Markdown endpoints (`/docs/.../index.md`).
2. Fetch the specific `/docs/.../index.md` page(s) matching the user's question.
3. **Offline fallback:** If `https://leapmux.dev/llms.txt` is unreachable (air-gapped host or no-network sandbox), fall back immediately to `leapmux guide`, `leapmux commands --compact --json`, and `leapmux <group> --help` bundled in the CLI binary.
4. Answer the user directly with concrete steps or commands — do not deflect by telling them to go read the docs.

## 2. Establish Topology Before Diagnosing

LeapMux separates three roles that may live on different machines:

- **Client:** Web browser, Desktop app, or `leapmux` CLI
- **Hub:** Local solo/dev Hub or remote Hub server (zero-knowledge relay)
- **Worker(s):** Machine(s) hosting workspaces, terminals, files, and coding-agent CLIs

Clarify which machine has the symptom before running commands so you never check the Client's shell `PATH` or logs when the issue is on a remote Worker.

## 3. Run Minimal Read-Only Diagnostics

When troubleshooting a live setup, start with read-only checks:

```sh
leapmux version
leapmux context show
leapmux whoami --json
leapmux doctor
leapmux worker list
leapmux agent providers
```

Compare CLI, Hub, and Worker versions when flags or RPCs fail unexpectedly.

## 4. Strict Safety & Privacy Guardrails

- **Never mutate without explicit user consent.** Do not edit config files, change credentials (`leapmux auth`), alter settings, open tunnels, or restart a Hub or Worker unless the user explicitly asks you to.
- **Warn before restarting the local Worker.** Restarting the Worker hosting your current tab terminates the diagnosing agent session.
- **Redact secrets before displaying or sharing output.** Strip bearer tokens, session cookies, private keys, recovery codes, and internal hostnames before pasting logs or drafting a GitHub issue (`https://github.com/leapmux/leapmux/issues`).
