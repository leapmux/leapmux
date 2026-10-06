/**
 * CLI helpers for end-to-end tests of leapmux control.
 * The launcher verifies the root leapmux binary through task build-backend once per run.
 * Tests use that binary for control and recover commands and parse its standard output as JSON.
 *
 * LEAPMUX_CONTROL_CONFIG_DIR selects a private credential directory instead of the default user directory.
 * mintCLITokenForAdmin requests a token through AdminUserService/IssueAPIToken and writes the credential file.
 * The resulting CLI requests use the same credential path as a user login, without repeating the OAuth login flow.
 */

import { execFile } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { promisify } from 'node:util'
import { elevateSessionViaAPI, getCurrentUser, TEST_ADMIN_PASSWORD } from './api'
import { createTestDirectory } from './runDirectory'
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
  /** Administrator user ID that the bearer token authenticates. */
  userID: string
}

/**
 * The hub details that mintCLITokenForAdmin requires.
 * ServerInfo, DevServerHandle, and MultiWorkerHarness satisfy this interface without an adapter.
 */
export interface CLITokenSource {
  /** http(s) URL the hub listens on. */
  hubUrl: string
  /** Session cookie (e.g. `leapmux-session=…`) for the admin user, whose password is TEST_ADMIN_PASSWORD. */
  adminToken: string
}

/**
 * Permissions for the test CLI credential, using the scope format from RFC 6749 section 3.3.
 * Admin procedures require admin scopes even when the credential belongs to an administrator. See operating/security.md.
 * The remaining scopes match the default grant. An unscoped elevated session can issue this requested set.
 * The request and credential file share this list, so auth status cannot report permissions absent from the stored grant.
 */
const E2E_CLI_SCOPES = [
  'account:read',
  'account:write',
  'workspace:read',
  'workspace:write',
  'worker:read',
  'worker:admin',
  'agent:read',
  'agent:write',
  'terminal:read',
  'terminal:write',
  'file:read',
  'git:read',
  'git:write',
  'tunnel:open',
  'admin:read',
  'admin:users',
  'admin:settings',
  'admin:workers',
]

/**
 * Issue an administrator API token through the hub and write it in a new private credential directory.
 * Return the directory and token for later CLI requests. This avoids repeating the device-code or local-redirect login flow.
 * The recover command supports bootstrap only. Token issuance uses AdminUserService/IssueAPIToken.
 * The token belongs to the user of the adminToken session, and the credential file targets source.hubUrl.
 */
export async function mintCLITokenForAdmin(source: CLITokenSource): Promise<CLIConfigDir> {
  const hubURL = source.hubUrl
  const cookie = source.adminToken
  const user = await mintStep('read the administrator', () => getCurrentUser(hubURL, cookie))
  if (!user.id || !user.username)
    throw new Error('mintCLITokenForAdmin: the session user has no ID or username.')
  const userID = user.id
  // IssueAPIToken and OAuth consent require a recent factor check. A session does not start elevated.
  // Elevate the issuing session before the request, as the OAuth consent flow does.
  // The request below gives the new credential a one-hour lifetime.
  await mintStep('elevate the issuing session', () => elevateSessionViaAPI(hubURL, cookie, TEST_ADMIN_PASSWORD))

  // Connect-JSON: the body is the message object directly (int64s as
  // strings), and the response JSON is the message object.
  const res = await fetch(`${hubURL}/leapmux.v1.AdminUserService/IssueAPIToken`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Cookie': cookie },
    body: JSON.stringify({
      userId: userID,
      // Identify this app installation. An app can hold separate credentials on multiple machines.
      installationName: `e2e-${Date.now()}`,
      ttlSeconds: '3600',
      // See E2E_CLI_SCOPES.
      scopes: E2E_CLI_SCOPES,
    }),
  })
  if (!res.ok) {
    // Include the response body in an issuance error.
    // Connect supplies the refusal message there. A status alone cannot identify which setup requirement failed.
    throw new Error(`mintCLITokenForAdmin: IssueAPIToken ${res.status}: ${await res.text()}`)
  }
  const minted = await res.json() as { accessToken?: string }
  const bearer = minted.accessToken
  if (!bearer) {
    throw new Error('mintCLITokenForAdmin: no accessToken in IssueAPIToken response')
  }

  // LEAPMUX_CONTROL_CONFIG_DIR selects the exact credential directory. Files reside directly below it.
  // Do not add control/. The CLI appends leapmux/control only when it derives the default from XDG_CONFIG_HOME.
  const configDir = createTestDirectory('leapmux-cli-cfg-')
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
    username: user.username,
    // This local scope list lets auth status describe the grant without a request.
    // The hub remains authoritative. Keep the list equal to the issuance request.
    scope: E2E_CLI_SCOPES.join(' '),
  }
  writeFileSync(credPath, JSON.stringify(cred, null, 2), { mode: 0o600 })

  // Elevate the new credential separately from the session that issued it.
  // Hub settings and some admin procedures check the acting credential, so the issuer elevation does not transfer.
  // The required approval uses a browser ceremony. The fixture performs that ceremony for the CLI.
  await elevateMintedCredential(hubURL, bearer, cookie)

  return { path: configDir, hubURL, bearer, userID }
}

/** Run one setup step of mintCLITokenForAdmin, and name the step in its failure. */
async function mintStep<T>(step: string, operation: () => Promise<T>): Promise<T> {
  try {
    return await operation()
  }
  catch (cause) {
    throw new Error(`mintCLITokenForAdmin: could not ${step}`, { cause })
  }
}

/**
 * Elevate a newly issued CLI credential through the real approval endpoints.
 * Request a user code from /oauth/step-up, then approve it at /oauth/device with an elevated session.
 * Use fetch for both requests. Direct database changes would leave this approval path untested.
 * The caller elevates the approving session before issuance and supplies that cookie here.
 */
async function elevateMintedCredential(hubUrl: string, bearer: string, cookie: string): Promise<void> {
  const started = await fetch(`${hubUrl}/oauth/step-up`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      'Authorization': `Bearer ${bearer}`,
    },
    body: new URLSearchParams({ installation_name: 'e2e-fixture' }).toString(),
  })
  if (!started.ok) {
    throw new Error(`mintCLITokenForAdmin: elevate-authorization ${started.status}: ${await started.text()}`)
  }
  const grant = await started.json() as { user_code?: string }
  if (!grant.user_code) {
    throw new Error('mintCLITokenForAdmin: elevate-authorization returned no user_code')
  }

  const approved = await fetch(`${hubUrl}/oauth/device`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Cookie': cookie },
    body: new URLSearchParams({ user_code: grant.user_code, decision: 'allow' }).toString(),
    redirect: 'manual',
  })
  // Require HTTP 200 from the approval endpoint.
  // An unconfirmed approval leaves the credential unable to call procedures that require elevation.
  if (approved.status !== 200) {
    throw new Error(`mintCLITokenForAdmin: activate ${approved.status}: ${await approved.text()}`)
  }
}

/**
 * Run leapmux control against the configured hub. Return the JSON data payload from standard output.
 * A CLIError preserves the error code and message for assertions.
 * Remove inherited LEAPMUX_CONTROL_* variables except the configured credential directory.
 * Otherwise, a local worker shell could change the test transport or authentication.
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
  while (i < args.length) {
    const arg = args[i]
    // i < args.length implies a defined element, so the undefined check is type-level only.
    if (arg === undefined || arg.startsWith('-'))
      break
    i++
  }
  return [...args.slice(0, i), '--hub', hubURL, ...args.slice(i)]
}

/**
 * Write one hub setting through control admin settings set.
 * Specify --hub before positional arguments. withHubFlag inserts before the first flag, but this command has only positional arguments.
 * Appending --hub after KEY and VALUE would make the Go parser treat it as another positional argument.
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
