/**
 * CLI runner helpers for `leapmux control` end-to-end tests.
 *
 * The leapmux binary is built once per Playwright run by global-setup
 * (`task build-backend`) and lives at the repo root. The same binary
 * serves the `control` and `recover` commands; tests invoke it
 * as a child process and parse its JSON-on-stdout contract.
 *
 * Auth model used by these helpers
 * --------------------------------
 * The CLI is the production path for external clients, so it expects
 * credentials on disk (`~/.config/leapmux/control/<host>.json`). The
 * `LEAPMUX_CONTROL_CONFIG_DIR` env var redirects that lookup to a
 * per-test directory; combined with `mintCLITokenForAdmin` (which
 * mints an api token over the hub's `AdminUserService/IssueAPIToken`
 * RPC and writes the resulting bearer into a credential file) this
 * lets a Playwright test drive the CLI exactly the way a user would
 * after running `leapmux control auth login`, without round-tripping
 * through the OAuth-style flow.
 */

import type { Page } from '@playwright/test'
import type { ChildProcess } from 'node:child_process'
import { execFile, spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { promisify } from 'node:util'
import { expect } from '@playwright/test'
import { getGlobalState } from './server'

const execFileAsync = promisify(execFile)

/** A short-lived directory holding the CLI's credentials and pin store. */
export interface CLIConfigDir {
  /** Absolute path written into `LEAPMUX_CONTROL_CONFIG_DIR`. */
  path: string
  /** Hub URL the credential file targets. */
  hubURL: string
  /** Bearer access token (visible for assertions; never logged). */
  bearer: string
  /** Numeric admin user ID the bearer authenticates as. */
  userID: string
}

/**
 * The minimal slice of a hub fixture `mintCLITokenForAdmin` needs.
 * Both the single-worker `ServerInfo` and the multi-worker harness
 * satisfy this — it's the smallest contract that lets a test hand
 * over "this is my hub URL, this is the cookie that talks to it,
 * here's the admin's credentials" without forcing the multi-worker
 * fixture to pretend it's a `ServerInfo`.
 */
export interface CLITokenSource {
  /** http(s) URL the hub listens on. */
  hubUrl: string
  /** Session cookie (e.g. `leapmux-session=…`) for the admin user. */
  adminToken: string
  /** Data dir the running hub opens. Kept for the interface's callers. */
  dataDir: string
  /** Admin password; when set the mint logs in itself for a fresh session. */
  adminPassword?: string
  /** Admin username; when set the mint logs in itself for a fresh session. */
  adminUsername?: string
}

/**
 * Mint an api_tokens row for the test hub's admin user over the hub's own
 * RPC surface, then write a credential file under a fresh per-test config
 * dir. Returns the dir + bearer so subsequent `runCLI` calls can
 * authenticate as the admin without going through the device-code or
 * local-redirect OAuth flows.
 *
 * The offline admin-token CLI command no longer exists (the `recover`
 * tree only bootstraps); the online path is `AdminUserService/IssueAPIToken`
 * with an admin session. The session comes from a fresh `AuthService/Login`
 * when the source carries credentials, falling back to the fixture's
 * `adminToken` cookie.
 */
export async function mintCLITokenForAdmin(source: CLITokenSource, options?: {
  /** Override the hub URL written into the credential file (defaults to source.hubUrl). */
  hubURL?: string
  /** User ID to mint the token for. Defaults to the admin user. */
  userID?: string
}): Promise<CLIConfigDir> {
  const userID = options?.userID ?? await fetchAdminUserID(source)
  const hubURL = options?.hubURL ?? source.hubUrl

  const cookie = source.adminPassword && source.adminUsername
    ? await loginForMint(source.hubUrl, source.adminUsername, source.adminPassword)
    : source.adminToken

  // Connect-JSON: the body is the message object directly (int64s as
  // strings), and the response JSON is the message object.
  const res = await fetch(`${source.hubUrl}/leapmux.v1.AdminUserService/IssueAPIToken`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
    body: JSON.stringify({
      userId: userID,
      clientType: 'cli',
      clientName: `e2e-${Date.now()}`,
      ttlSeconds: '3600',
    }),
  })
  if (!res.ok) {
    throw new Error(`mintCLITokenForAdmin: IssueAPIToken ${res.status}`)
  }
  const minted = await res.json() as { accessToken?: string }
  const bearer = minted.accessToken
  if (!bearer) {
    throw new Error('mintCLITokenForAdmin: no accessToken in IssueAPIToken response')
  }

  // `LEAPMUX_CONTROL_CONFIG_DIR` returns the directory the CLI uses
  // verbatim — credentials live as `<dir>/<hub-host>.json` inside it.
  // Don't introduce an extra `control/` subdir: that's only present in
  // the default `~/.config/leapmux/control/` layout, where the CLI
  // appends `/leapmux/control` itself when only `XDG_CONFIG_HOME` is
  // set.
  const configDir = mkdtempSync(join(tmpdir(), 'leapmux-cli-cfg-'))
  mkdirSync(configDir, { recursive: true })

  // The CLI keys the credential file by HubHost(hubURL); replicate
  // that here. For http(s) URLs the host is `<host>_<port>`; for
  // unix:/npipe: sockets the URL is flattened.
  const hubHost = hubHostForURL(hubURL)
  const credPath = join(configDir, `${hubHost}.json`)
  const cred = {
    hub_url: hubURL,
    hub_id: 'e2e',
    access_token: bearer,
    refresh_token: '',
    expires_at: new Date(Date.now() + 3_600_000).toISOString(),
    user_id: userID,
    username: 'admin',
  }
  writeFileSync(credPath, JSON.stringify(cred, null, 2), { mode: 0o600 })

  return { path: configDir, hubURL, bearer, userID }
}

/**
 * A fresh admin session for the mint: POST the Connect-JSON Login and keep
 * the `leapmux-session=…` Set-Cookie value verbatim.
 */
async function loginForMint(hubUrl: string, username: string, password: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/Login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
    redirect: 'manual',
  })
  if (!res.ok) {
    throw new Error(`mintCLITokenForAdmin: Login ${res.status}`)
  }
  const setCookie = res.headers.get('set-cookie')
  if (!setCookie?.includes('leapmux-session=')) {
    throw new Error('mintCLITokenForAdmin: no session cookie in Login response')
  }
  for (const part of setCookie.split(';')) {
    const trimmed = part.trim()
    if (trimmed.startsWith('leapmux-session='))
      return trimmed
  }
  throw new Error('mintCLITokenForAdmin: malformed session cookie in Login response')
}

/**
 * Run `leapmux control …` against the cfg dir's hub.
 *
 * Returns the parsed JSON `data` payload from stdout. CLI errors are
 * thrown as `CLIError` carrying the upstream `code` and `message` so
 * test assertions can match on either (e.g.
 * `await expect(...).rejects.toMatchObject({ code: 'out_of_date' })`).
 *
 * The `LEAPMUX_CONTROL_*` env vars are scrubbed (except
 * `LEAPMUX_CONTROL_CONFIG_DIR`) so a test running on a laptop that
 * happens to have an active worker shell can't pollute the harness's
 * auth context.
 */
export async function runCLI(cfg: CLIConfigDir, args: string[], options?: {
  /** Extra env vars merged into the CLI's environment. */
  env?: Record<string, string>
  /** Soft timeout in ms; defaults to 30s. */
  timeoutMs?: number
}): Promise<unknown> {
  const { binaryPath } = getGlobalState()
  const env = scrubLeapMuxEnv({
    ...process.env,
    ...options?.env,
    LEAPMUX_CONTROL_CONFIG_DIR: cfg.path,
  })
  // `--hub` is a leaf-command flag, not top-level. The first
  // non-flag tokens in `args` walk the control command tree
  // (e.g. ["agent","open"]); we splice `--hub <url>` AFTER that
  // walk so the dispatcher reaches the leaf before parsing flags.
  const cliArgs = withHubFlag(args, cfg.hubURL)
  try {
    const { stdout } = await execFileAsync(binaryPath, ['control', ...cliArgs], {
      env,
      timeout: options?.timeoutMs ?? 30_000,
    })
    return parseEnvelope(stdout, args)
  }
  catch (err) {
    // execFileAsync rejects with stdout/stderr attached when the
    // child exits non-zero. The CLI writes the JSON `{"error": …}`
    // envelope to stdout (same channel as success) and a non-zero
    // exit code is the only signal of failure; only fall back to
    // stderr for catastrophic failures that bypassed EmitError.
    const e = err as { stdout?: string, stderr?: string, code?: number | string, message?: string }
    if (e.stdout) {
      try {
        return parseEnvelope(e.stdout, args)
      }
      catch (parseErr) {
        if (parseErr instanceof CLIError)
          throw parseErr
        // fall through to the catastrophic-error path
      }
    }
    throw new Error(`leapmux control ${args.join(' ')} exit=${e.code}: ${e.message}\nstdout: ${e.stdout ?? ''}\nstderr: ${e.stderr ?? ''}`)
  }
}

/**
 * Spawn a long-running CLI subcommand (e.g. `events`,
 * `agent messages --follow`) and return the child process plus an
 * async iterator over JSON-line events. The caller is responsible for
 * killing the process when done.
 */
export function streamCLI(cfg: CLIConfigDir, args: string[]): {
  child: ChildProcess
  events: AsyncIterable<unknown>
  done: Promise<void>
} {
  const { binaryPath } = getGlobalState()
  const env = scrubLeapMuxEnv({
    ...process.env,
    LEAPMUX_CONTROL_CONFIG_DIR: cfg.path,
  })
  const child = spawn(binaryPath, ['control', ...withHubFlag(args, cfg.hubURL)], { env })

  const events = (async function* () {
    let buf = ''
    for await (const chunk of child.stdout!) {
      buf += chunk.toString()
      let nl = buf.indexOf('\n')
      while (nl !== -1) {
        const line = buf.slice(0, nl).trim()
        buf = buf.slice(nl + 1)
        nl = buf.indexOf('\n')
        if (!line)
          continue
        try {
          yield JSON.parse(line) as unknown
        }
        catch {
          // Skip non-JSON lines (e.g. logs leaking to stdout).
        }
      }
    }
  })()

  const done = new Promise<void>((resolve) => {
    child.on('close', () => resolve())
  })

  return { child, events, done }
}

/**
 * Run `leapmux control tab open --type=agent` and return the tab_id
 * the hub minted. The CLI envelope is `{"data": ...}` where the
 * payload has snake_case keys including `tab_id`, `workspace_id`,
 * `worker_id`.
 */
export async function cliAgentOpen(cli: CLIConfigDir, params: {
  workspaceId: string
  workerId: string
  provider?: string
}): Promise<string> {
  // Dev-mode workers register every provider they detect on PATH, so
  // the CLI rejects `tab open` with `ambiguous_provider` unless one is
  // specified. Default to Claude Code (matches `LEAPMUX_CLAUDE_DEFAULT_MODEL`
  // in the dev fixture) so existing call sites keep working.
  const provider = params.provider ?? 'claude'
  const data = await runCLI(cli, [
    'tab',
    'open',
    '--type',
    'agent',
    '--workspace-id',
    params.workspaceId,
    '--worker-id',
    params.workerId,
    '--provider',
    provider,
  ]) as { tab_id?: string, id?: string } | null
  const id = data?.tab_id ?? data?.id
  if (!id || typeof id !== 'string')
    throw new Error(`cliAgentOpen: missing tab_id in response: ${JSON.stringify(data)}`)
  return id
}

/**
 * Wait for `count` agent tabs to render. Dev mode boots the worker
 * subprocess lazily so the first render after seeding can take a beat
 * longer than the default action timeout; 60s matches the budget the
 * control-CLI specs use for their worker-spawn / broadcast assertions.
 */
export async function waitForAgentTabs(page: Page, count: number) {
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]'))
    .toHaveCount(count)
}

export class CLIError extends Error {
  constructor(public readonly args: string[], public readonly code: string, message: string) {
    super(`leapmux control ${args.join(' ')} failed: ${code}: ${message}`)
    this.name = 'CLIError'
  }
}

// ──────────────────────────────────────────────
// Internals
// ──────────────────────────────────────────────

/**
 * Resolve the admin user's ID by hitting the hub's GetCurrentUser
 * endpoint with the seeded admin cookie. This is one fetch in setup
 * land and avoids re-implementing the admin-bootstrap query.
 */
async function fetchAdminUserID(source: CLITokenSource): Promise<string> {
  const res = await fetch(`${source.hubUrl}/leapmux.v1.AuthService/GetCurrentUser`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Cookie': source.adminToken },
    body: '{}',
  })
  if (!res.ok)
    throw new Error(`fetchAdminUserID: GetCurrentUser ${res.status}`)
  const data = await res.json() as { user?: { id?: string } }
  if (!data.user?.id)
    throw new Error('fetchAdminUserID: no user.id in response')
  return data.user.id
}

/**
 * Parse the CLI's JSON envelope. Both success (`{"data": …}`) and
 * failure (`{"error": …}`) envelopes go to stdout; the only signal
 * of failure is the process exit code, so callers should still trap
 * non-zero exits before invoking this on the rejection path.
 *
 * Throws CLIError when the envelope carries `error.code`; throws a
 * generic Error when stdout isn't a recognisable envelope at all.
 */
function parseEnvelope(stdout: string, args: string[]): unknown {
  const trimmed = stdout.trim()
  if (!trimmed)
    throw new Error(`leapmux control ${args.join(' ')}: empty stdout`)
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  }
  catch (err) {
    throw new Error(`leapmux control ${args.join(' ')}: stdout is not JSON:\n${trimmed}\n\nparse error: ${(err as Error).message}`)
  }
  if (parsed && typeof parsed === 'object') {
    if ('error' in parsed) {
      const e = (parsed as { error: { code?: string, message?: string } }).error
      throw new CLIError(args, e.code ?? 'unknown', e.message ?? 'unknown error')
    }
    if ('data' in parsed)
      return (parsed as { data: unknown }).data
  }
  // Some commands stream raw payloads without the data wrapper
  // (e.g. `events` writes JSON-line events). Return as-is for those.
  return parsed
}

/**
 * Splice `--hub <url>` into args AFTER the leading
 * command-tree tokens (`agent open`, `tab close`, …). The control
 * dispatcher rejects flags at the group level — it walks the tree
 * to a leaf first — so passing `--hub` before the leaf fails with
 * "unknown control command: --hub". Existing `--hub` tokens take
 * precedence: the helper only inserts when the caller didn't
 * provide one.
 */
function withHubFlag(args: string[], hubURL: string): string[] {
  if (args.includes('--hub'))
    return args
  let i = 0
  while (i < args.length && !args[i].startsWith('-'))
    i++
  return [...args.slice(0, i), '--hub', hubURL, ...args.slice(i)]
}

/**
 * Drop LEAPMUX_CONTROL_* env vars so a developer's local agent shell
 * doesn't accidentally short-circuit the CLI's transport selection
 * (e.g. spawning the CLI from an active LeapMux agent would otherwise
 * direct calls at the per-agent unix socket instead of the test
 * hub).
 */
function scrubLeapMuxEnv(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const out: NodeJS.ProcessEnv = { ...env }
  for (const k of Object.keys(out)) {
    if (k.startsWith('LEAPMUX_CONTROL_') && k !== 'LEAPMUX_CONTROL_CONFIG_DIR')
      delete out[k]
  }
  delete out.LEAPMUX_HUB
  return out
}

/**
 * Mirror `(control.HubHost)` from the Go CLI so the credential
 * filename produced here is the one the CLI will look up.
 */
function hubHostForURL(hubURL: string): string {
  if (hubURL.startsWith('unix:') || hubURL.startsWith('npipe:'))
    return hubURL.replace(/\//g, '_').replace(/:/g, '_').replace(/\\/g, '_')
  const url = new URL(hubURL)
  const host = url.hostname
  if (!host)
    throw new Error(`hubHostForURL: missing hostname in ${hubURL}`)
  return url.port ? `${host}_${url.port}` : host
}
