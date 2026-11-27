# LeapMux Diagnostics Checklist

Use only read-only checks unless the user explicitly approves a mutating fix.

## Symptom-to-Command Matrix

| Symptom | First Read-Only Checks | What to Verify |
|---|---|---|
| CLI cannot connect / auth failure | `leapmux whoami --json`, `leapmux context show`, `leapmux doctor` | Inside a tab (`$LEAPMUX_CONTROL_SOCK`) vs. outside (`--hub`, `$LEAPMUX_HUB`, stored credentials) |
| Remote Worker offline or missing | `leapmux worker list`, `leapmux doctor` | Online status, Worker ID/name, version mismatch with Hub/CLI |
| Coding agent provider missing or unavailable | `leapmux agent providers` | Which providers the target Worker detected under its service `PATH` |
| Agent stuck or waiting unexpectedly | `leapmux agent list` (or `leapmux agent get <agent>`), `leapmux agent requests <agent>`, `leapmux agent messages <agent> --limit 5 --markdown` | `waiting_for_user` permission/question prompt vs. active turn |
| Skills missing or drifted on a Worker | `leapmux skill status` or `leapmux skill status --worker <worker>` | Target directory (`~/.agents/skills`, `~/.claude/skills`, etc.) and `modified`/`outdated` state |
| Version skew between CLI and Hub/Worker | `leapmux version` | Compare against Hub/Worker build version |

## Redacted Issue Report Template

Before suggesting the user open an issue at `https://github.com/leapmux/leapmux/issues`, help prepare a minimal redacted summary:

```markdown
- LeapMux CLI / Hub / Worker versions: `<output of leapmux version>`
- Topology: `<Solo Desktop | Self-hosted Hub + Remote Worker | CLI>`
- OS & shell (Client + Worker): `<os / arch>`
- Exact command or UI step: `<redacted command>`
- Observed behavior & exit code: `<redacted error>`
- Expected behavior: `<expected outcome>`
```
