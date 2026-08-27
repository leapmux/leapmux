/**
 * CLI runner helpers for `leapmux control` end-to-end tests.
 *
 * Global setup builds the leapmux binary once per Playwright run
 * (`task build-backend`), and it lives at the repo root. The same binary
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
import { TEST_ADMIN_PASSWORD } from './api'
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
  // Issuing an admin-scoped CLI credential is an elevated-only action, the
  // same as every /auth/cli/* consent leg: it mints a bearer that outlives
  // the browser session by a year. So the mint elevates first, exactly as a
  // person at the verification screen does.
  //
  // BOTH branches, and that is the point: the fallback branch reuses a
  // fixture cookie minted by sign-up, which is no more elevated than a fresh
  // login. Elevating only the login branch left every dev-server spec
  // failing in setup with a refusal that specified no verb.
  //
  // The password defaults to the seeded admin's, because every E2E hub seeds
  // that one account -- a source that carries its own credentials states
  // them, and the rest share the fixture's.
  await elevateForMint(source.hubUrl, cookie, source.adminPassword ?? TEST_ADMIN_PASSWORD)

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
      // The specs that drive `control admin ...` need it: the hub refuses an
      // ordinary CLI credential every Admin* procedure, even for an
      // administrator. See operating/security.md on why that is the default.
      adminScope: true,
    }),
  })
  if (!res.ok) {
    // The BODY, not the status alone. A Connect error carries its message
    // there, and a bare "IssueAPIToken 400" says nothing about which of the
    // several refusals fired -- which is exactly the diagnosis this helper's
    // callers need, because they all fail in setup.
    throw new Error(`mintCLITokenForAdmin: IssueAPIToken ${res.status}: ${await res.text()}`)
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
  // unix:/npipe: sockets the helper flattens the URL.
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
    admin_scope: true,
  }
  writeFileSync(credPath, JSON.stringify(cred, null, 2), { mode: 0o600 })

  // The CREDENTIAL needs its own window, not just the session that minted it.
  //
  // Every hub-settings write and several admin verbs run under the elevation
  // gate, and the gate reads the ACTING credential -- so a bearer minted by an
  // elevated session is still un-elevated, and `admin settings set` answers
  // "this action needs a recent sign-in". A CLI cannot clear that on its own
  // here: the hub's remedy is a browser ceremony, and an E2E worker has no
  // person at a keyboard.
  await elevateMintedCredential(source.hubUrl, bearer, cookie)

  return { path: configDir, hubURL, bearer, userID }
}

/**
 * Run the browser step-up ceremony for a freshly minted CLI credential.
 *
 * The REAL leg, end to end, because a fixture that stamped the row directly
 * would let the ceremony rot: the CLI posts
 * `/auth/cli/elevate-authorization` for a user code, and the person approves
 * that code at `/auth/cli/activate` from an already elevated browser session.
 * This does both halves with `fetch`, which is what a person does with a
 * browser.
 *
 * The approving session must itself be elevated, which is why the caller
 * elevates it before the mint and this reuses that cookie.
 */
async function elevateMintedCredential(hubUrl: string, bearer: string, cookie: string): Promise<void> {
  const started = await fetch(`${hubUrl}/auth/cli/elevate-authorization`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      'Authorization': `Bearer ${bearer}`,
    },
    body: new URLSearchParams({ device_name: 'e2e-fixture' }).toString(),
  })
  if (!started.ok) {
    throw new Error(`mintCLITokenForAdmin: elevate-authorization ${started.status}: ${await started.text()}`)
  }
  const grant = await started.json() as { user_code?: string }
  if (!grant.user_code) {
    throw new Error('mintCLITokenForAdmin: elevate-authorization returned no user_code')
  }

  const approved = await fetch(`${hubUrl}/auth/cli/activate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Cookie': cookie },
    body: new URLSearchParams({ user_code: grant.user_code }).toString(),
    redirect: 'manual',
  })
  // The page answers 200 with the "verified" body. Anything else means the
  // approval did not land, and the credential then fails every gated verb
  // with a refusal that names a browser the spec does not have.
  if (approved.status !== 200) {
    throw new Error(`mintCLITokenForAdmin: activate ${approved.status}: ${await approved.text()}`)
  }
}

/**
 * Prove a factor on the session the mint is about to use.
 *
 * Signing in is not enough: the hub refuses IssueAPIToken from a session that
 * did not prove a factor recently, which is the same rule every
 * `/auth/cli/*` consent leg applies. Elevating here is the honest fixture —
 * it is the step the browser takes before the same screen appears.
 *
 * Re-elevating an already-elevated session is harmless: the grant replaces
 * whatever window the session held.
 */
async function elevateForMint(hubUrl: string, cookie: string, password: string): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/ElevateSession`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
    body: JSON.stringify({ currentPassword: password }),
  })
  if (!res.ok) {
    throw new Error(`mintCLITokenForAdmin: ElevateSession ${res.status}: ${await res.text()}`)
  }
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
 * Returns the parsed JSON `data` payload from stdout. This helper throws a
 * CLI error as a `CLIError` that carries the upstream `code` and `message`,
 * so test assertions can match on either (e.g.
 * `await expect(...).rejects.toMatchObject({ code: 'out_of_date' })`).
 *
 * This helper also scrubs the `LEAPMUX_CONTROL_*` env vars (except
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
 * async iterator over JSON-line events. The caller must kill the process
 * when it finishes.
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
  // the CLI rejects `tab open` with `ambiguous_provider` unless the caller
  // specifies one. Default to Claude Code (matches `LEAPMUX_CLAUDE_DEFAULT_MODEL`
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
 * subprocess lazily, so the first render after seeding can take a little
 * longer than an ordinary action; the global expect timeout in
 * `playwright.config.ts` covers it.
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
 * Resolve the admin user's ID by calling the hub's GetCurrentUser
 * endpoint with the seeded admin cookie. This is one fetch during setup,
 * and it avoids re-implementing the admin-bootstrap query.
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
 * Write one hub setting through `control admin settings set`.
 *
 * This helper spells `--hub` out rather than leaving it to withHubFlag. That
 * helper inserts the flag before the FIRST token that starts with `-`, and
 * this verb has none — so the flag arrived after KEY and VALUE, where Go's flag
 * parser already stopped looking, and the CLI counted three positionals and
 * printed its usage line.
 */
export async function setHubSetting(cfg: CLIConfigDir, key: string, value: string): Promise<void> {
  await runCLI(cfg, ['admin', 'settings', 'set', '--hub', cfg.hubURL, key, value])
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
