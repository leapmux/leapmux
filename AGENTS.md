# AGENTS.md

## Cursor Cloud specific instructions

LeapMux is a Go backend (the unified `leapmux` binary = Hub + Worker) plus a
SolidJS/Vinxi frontend, an optional Tauri desktop app, and a Hugo docs site.
Standard build/lint/test/run commands live in `README.md`, `CLAUDE.md`, and
`Taskfile.yaml` (use `task` targets, e.g. `task lint`, `task test`,
`task dev`); this section only records non-obvious, durable caveats for
running things in the Cursor Cloud VM.

### Services and how to run them for local dev

Core product = **backend + frontend** (desktop app and docs site are optional).
- Backend (Hub + embedded Worker), single-user, no login: `./leapmux solo -dev-frontend http://localhost:4328` (after `task build-backend`), listens on `127.0.0.1:4327`.
- Frontend dev server: `cd frontend && bun run dev` (Vinxi on port `4328`, proxies `/leapmux.v1` API calls to `4327`).
- `task dev` / `task dev-solo` launch both together via `mprocs` (a TUI multiplexer). `mprocs` is interactive, so for headless/background use run the two processes directly (each in its own tmux session) instead.
- Open the app at http://localhost:4327 (or http://localhost:4328). Solo mode auto-creates a `solo` user; no login needed.

### Non-obvious caveats (learned during setup)

- **Long-lived processes must run inside tmux as a foreground/session command.** Backgrounding a server from a single shell command gets it killed when the command returns, and the harness reaps *idle* tmux sessions — so a session that just holds a bare shell can disappear. Create the session with the server as its command (or start the server as the first foreground command) so the session stays busy. Don't send the server stop signals.
- **Never `pkill -f "leapmux …"` with a pattern that also appears in your own command line** — `pkill -f` matches the shell running your command and kills it (silent, instant exit). Use `pkill -x leapmux` (exact process-name match) instead.
- **"No workers online" when opening a terminal/agent, even though a workspace was created:** the frontend pins each Worker's E2EE public key on first use (TOFU) in browser storage. If the Worker's key changes (e.g. you wiped `solo`'s `-data-dir`, which regenerates the embedded worker's keypair), the stale pin makes the Worker show offline for opening terminals/agents. Fix: use a fresh browser / Incognito window (or clear the site's storage), or accept the "Worker public key changed" dialog if it appears. Prefer **not** wiping the solo data dir between runs.
- **Don't run `leapmux admin` / `leapmux remote` against the same SQLite data dir while `leapmux solo` (or the Hub) is running.** LeapMux allows only one active Hub per database; concurrent CLI access to the live DB can wedge the running Hub (RPCs start hanging). `admin` uses `<data-dir>/hub.db` while `solo` writes `<data-dir>/hub/hub.db` — mind the path difference.
- **Agent providers need their CLI installed on the Worker.** Claude Code, Codex, Cursor, etc. are not installed in this VM, so creating a workspace with an agent shows the agent pane erroring on startup — that is expected and not an environment problem. **Shell terminals work without any provider CLI**, so they're the reliable way to exercise the Worker end-to-end (create a workspace on a git repo, then open a terminal and run a command).
- **Node:** a system `/exec-daemon/node` (v22) shadows nvm's Node on `PATH`. The repo targets Node 24, but v22 works for building, testing, and running; `bun` is the real frontend runtime.
- **Post-quantum E2EE handshakes are CPU-heavy.** In this constrained VM the default `-encryption-mode post-quantum` handshake can be slow; `-encryption-mode classic` is faster for local dev if channel setup feels sluggish.
- **Backend integration tests are slow here** (`task test-backend` runs with `-tags integration`; some packages spawn `git` many times, which is slow in this VM). Use `task test-backend-no-docker` — the Docker-backed storage backends (postgres/mysql/cockroachdb/tidb/yugabytedb) are not set up in this VM.
