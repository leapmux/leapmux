/**
 * CLI helpers for end-to-end tests of the leapmux CLI.
 * The launcher verifies the root leapmux binary through task build-backend once per run.
 * Tests use that binary for control and recover commands and parse its standard output as JSON.
 *
 * LEAPMUX_CONTROL_CONFIG_DIR selects a private credential directory instead of the default user directory.
 * mintCLITokenForAdmin requests a token through AdminUserService/IssueAPIToken and writes the credential file.
 * The resulting CLI requests use the same credential path as a user login, without repeating the OAuth login flow.
 */

import { execFile, spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { promisify } from 'node:util'
import { callHub, elevateSessionViaAPI, getCurrentUser, TEST_ADMIN_PASSWORD } from './api'
import { fetchHub } from './hubTransport'
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
 * Admin procedures require admin scopes even when the credential belongs to an administrator. See site/content/docs/admin/security-and-auth.md.
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
  'admin:apps',
]

/**
 * Issue an administrator API token through the hub and write it in a new private credential directory.
 * Return the directory and token for later CLI requests. This avoids repeating the device-code or local-redirect login flow.
 * The recover command supports bootstrap only. Token issuance uses AdminUserService/IssueAPIToken.
 * The token belongs to the user of the adminToken session, and the credential file targets source.hubUrl.
 */
export async function mintCLITokenForAdmin(
  source: CLITokenSource,
  options: { scopes?: readonly string[], elevate?: boolean } = {},
): Promise<CLIConfigDir> {
  const hubURL = source.hubUrl
  const cookie = source.adminToken
  const scopes = options.scopes ?? E2E_CLI_SCOPES
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
  // A refusal states the hub's reason, which identifies the setup requirement that failed.
  const minted = await callHub<{ accessToken?: string }>(hubURL, 'AdminUserService/IssueAPIToken', {
    user: { id: userID },
    // Identify this app installation. An app can hold separate credentials on multiple machines.
    installationName: `e2e-${Date.now()}`,
    ttlSeconds: '3600',
    // See E2E_CLI_SCOPES.
    scopes,
  }, { cookie, operation: 'mintCLITokenForAdmin' })
  const bearer = minted.accessToken
  if (!bearer) {
    throw new Error('mintCLITokenForAdmin: no accessToken in IssueAPIToken response')
  }

  // LEAPMUX_CONTROL_CONFIG_DIR selects the control CLI config root. Credential
  // files live under `<configDir>/credentials/<hubHost>.json` (mirroring
  // `control.CredentialsPath` in backend/internal/cli/control/creds.go).
  const configDir = createTestDirectory('leapmux-cli-cfg-')
  const credsDir = join(configDir, 'credentials')
  mkdirSync(credsDir, { recursive: true })

  // The CLI keys the credential file by HubHost(hubURL); replicate
  // that here. For http(s) URLs the host is `<host>_<port>`; for
  // unix:/npipe: sockets the helper flattens the URL.
  const hubHost = hubHostForURL(hubURL)
  const credPath = join(credsDir, `${hubHost}.json`)
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
    scope: scopes.join(' '),
  }
  writeFileSync(credPath, JSON.stringify(cred, null, 2), { mode: 0o600 })

  // Elevate the new credential separately from the session that issued it.
  // Hub settings and some admin procedures check the acting credential, so the issuer elevation does not transfer.
  // The required approval uses a browser ceremony. The fixture performs that ceremony for the CLI.
  if (options.elevate ?? true)
    await elevateCLICredential(hubURL, bearer, cookie)

  return {
    path: configDir,
    hubURL,
    bearer,
    userID,
  }
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
 * Read the saved `access_token` from a CLI credential directory for `hubURL`.
 */
export function readCLICredentialToken(configDir: string, hubURL: string): string {
  const credPath = join(configDir, 'credentials', `${hubHostForURL(hubURL)}.json`)
  const saved = JSON.parse(readFileSync(credPath, 'utf8')) as { access_token?: string }
  if (!saved.access_token)
    throw new Error(`readCLICredentialToken: saved credential at ${credPath} has no access_token`)
  return saved.access_token
}

/**
 * Elevate a CLI credential through the real approval endpoints.
 * Request a user code from /oauth/step-up, then approve it at /oauth/device with an elevated session.
 * Use fetch for both requests. Direct database changes would leave this approval path untested.
 * The caller elevates the approving session before issuance and supplies that cookie here.
 */
export async function elevateCLICredential(hubUrl: string, bearer: string, cookie: string): Promise<void> {
  const started = await fetchHub(`${hubUrl}/oauth/step-up`, {
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

  const approved = await fetchHub(`${hubUrl}/oauth/device`, {
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
 * Approve an OAuth device-code authorization request (for `leapmux auth login --device-code`)
 * against the real Hub `/oauth/device` endpoint using an authenticated browser session cookie.
 */
export async function approveOAuthDeviceCode(hubUrl: string, cookie: string, userCode: string): Promise<void> {
  const approved = await fetchHub(`${hubUrl}/oauth/device`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Cookie': cookie },
    body: new URLSearchParams({
      user_code: userCode,
      decision: 'allow',
      admin_confirmed: '1',
    }).toString(),
    redirect: 'manual',
  })
  if (approved.status !== 200) {
    throw new Error(`approveOAuthDeviceCode: /oauth/device returned ${approved.status}: ${await approved.text()}`)
  }
}

/**
 * Sign the real `leapmux` CLI in via interactive OAuth device-code flow
 * (`leapmux auth login --device-code`) and optionally elevate the resulting
 * credential. Unlike `mintCLITokenForAdmin` (which issues a service-account API
 * token that Hub policy excludes from durable admin actions like `admin user`
 * or `admin app`), this yields a full interactive OAuth CLI credential.
 */
export async function mintInteractiveOAuthCLI(
  source: CLITokenSource,
  options: {
    scopes?: readonly string[]
    elevate?: boolean
    password?: string
    deviceName?: string
  } = {},
): Promise<CLIConfigDir> {
  const hubURL = source.hubUrl
  const cookie = source.adminToken
  const scopes = options.scopes ?? E2E_CLI_SCOPES
  const password = options.password ?? TEST_ADMIN_PASSWORD
  const user = await mintStep('read the signed-in user', () => getCurrentUser(hubURL, cookie))
  if (!user.id)
    throw new Error('mintInteractiveOAuthCLI: the session user has no ID.')
  await mintStep('elevate the browser session', () => elevateSessionViaAPI(hubURL, cookie, password))

  const configDir = createTestDirectory('leapmux-cli-oauth-')
  const loginArgs = [
    'auth',
    'login',
    '--hub',
    hubURL,
    '--device-code',
    '--device-name',
    options.deviceName ?? 'e2e-oauth-cli',
    '--scope',
    scopes.join(' '),
  ]
  const proc = spawnCLILive(null, loginArgs, {
    env: { LEAPMUX_CONTROL_CONFIG_DIR: configDir },
  })
  try {
    const deadline = Date.now() + 20_000
    let userCode: string | null = null
    while (Date.now() < deadline) {
      const text = `${proc.stdout()}\n${proc.stderr()}`
      const match = /user_code=([A-Z0-9-]+)/.exec(text) ?? /Enter the code:\s*([A-Z0-9-]+)/.exec(text)
      if (match?.[1]) {
        userCode = match[1]
        break
      }
      if (proc.child.exitCode !== null)
        throw new Error(`mintInteractiveOAuthCLI: auth login exited early (${proc.child.exitCode}):\n${proc.diagnostic()}`)
      await new Promise(resolve => setTimeout(resolve, 50))
    }
    if (!userCode)
      throw new Error(`mintInteractiveOAuthCLI: no device user code printed:\n${proc.diagnostic()}`)
    await approveOAuthDeviceCode(hubURL, cookie, userCode)
    const done = await proc.finished
    if (done.code !== 0)
      throw new Error(`mintInteractiveOAuthCLI: auth login failed (${done.code}):\n${proc.diagnostic()}`)
  }
  finally {
    await proc.stop()
  }

  const bearer = readCLICredentialToken(configDir, hubURL)

  if (options.elevate ?? true)
    await elevateCLICredential(hubURL, bearer, cookie)

  return {
    path: configDir,
    hubURL,
    bearer,
    userID: user.id,
  }
}

/**
 * Run leapmux against the configured hub with --json. Return the data payload of the JSON envelope on standard output.
 * A CLIError preserves the error code, message and hints for assertions.
 * Remove inherited LEAPMUX_CONTROL_* variables except the configured credential directory.
 * Otherwise, a local worker shell could change the test transport or authentication.
 */
export async function runCLI(cfg: CLIConfigDir, args: string[], options?: {
  /** Extra env vars merged into the CLI's environment. */
  env?: Record<string, string>
  /** Soft timeout in ms; defaults to 30s. */
  timeoutMs?: number
}): Promise<unknown> {
  const { binaryPath, cliArgs, env } = controlProcess(cfg, args, options?.env)
  try {
    const { stdout } = await execFileAsync(binaryPath, cliArgs, {
      env,
      timeout: options?.timeoutMs ?? 30_000,
    })
    return parseEnvelope(stdout, args)
  }
  catch (err) {
    // execFileAsync rejects with stdout/stderr attached when the
    // child exits non-zero. With --json the CLI writes the `{"error": …}`
    // envelope to stdout (same channel as success) and a non-zero
    // exit code is the only signal of failure; only fall back to
    // stderr for a failure that printed no envelope.
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
    throw new Error(`leapmux ${args.join(' ')} exit=${e.code}: ${e.message}\nstdout: ${e.stdout ?? ''}\nstderr: ${e.stderr ?? ''}`)
  }
}

/** Raw stdout, stderr, and exit code from a text-mode CLI invocation. */
export interface CLITextResult {
  code: number
  stdout: string
  stderr: string
}

/**
 * Run leapmux against the configured hub in text mode (without `--json`).
 * Returns stdout, stderr, and exit code without throwing on non-zero exit when `allowNonZero` is set.
 */
export async function runCLIText(cfg: CLIConfigDir, args: string[], options?: {
  env?: Record<string, string>
  cwd?: string
  allowNonZero?: boolean
}): Promise<CLITextResult> {
  const { binaryPath } = getGlobalState()
  const env = {
    ...scrubLeapMuxEnv(process.env),
    ...options?.env,
    LEAPMUX_CONTROL_CONFIG_DIR: cfg.path,
  }
  const cliArgs = ['--hub', cfg.hubURL, ...args]
  return execTextCLI(binaryPath, cliArgs, env, options?.cwd, options?.allowNonZero ?? false)
}

/**
 * Run a local-only leapmux command (no `--hub` flag) with `--json`, returning its decoded `data` payload.
 */
export async function runCLILocal(args: string[], options?: {
  configDir?: string
  env?: Record<string, string>
  cwd?: string
}): Promise<unknown> {
  const { binaryPath } = getGlobalState()
  const configDir = options?.configDir ?? createTestDirectory('leapmux-cli-local-')
  const env = {
    ...scrubLeapMuxEnv(process.env),
    ...options?.env,
    LEAPMUX_CONTROL_CONFIG_DIR: configDir,
  }
  const cliArgs = ['--json', ...args]
  try {
    const { stdout } = await execFileAsync(binaryPath, cliArgs, {
      env,
      cwd: options?.cwd,
      timeout: 30_000,
    })
    return parseEnvelope(stdout, args)
  }
  catch (err) {
    const e = err as { stdout?: string, stderr?: string, code?: number | string, message?: string }
    if (e.stdout) {
      try {
        return parseEnvelope(e.stdout, args)
      }
      catch (parseErr) {
        if (parseErr instanceof CLIError)
          throw parseErr
      }
    }
    throw new Error(`leapmux ${args.join(' ')} exit=${e.code}: ${e.message}\nstdout: ${e.stdout ?? ''}\nstderr: ${e.stderr ?? ''}`)
  }
}

/**
 * Run a local-only leapmux command (no `--hub`, no `--json`) and return raw stdout, stderr, and exit code.
 */
export async function runCLILocalText(args: string[], options?: {
  configDir?: string
  env?: Record<string, string>
  cwd?: string
  allowNonZero?: boolean
}): Promise<CLITextResult> {
  const { binaryPath } = getGlobalState()
  const configDir = options?.configDir ?? createTestDirectory('leapmux-cli-local-text-')
  const env = {
    ...scrubLeapMuxEnv(process.env),
    ...options?.env,
    LEAPMUX_CONTROL_CONFIG_DIR: configDir,
  }
  return execTextCLI(binaryPath, args, env, options?.cwd, options?.allowNonZero ?? false)
}

async function execTextCLI(
  binaryPath: string,
  cliArgs: string[],
  env: NodeJS.ProcessEnv,
  cwd: string | undefined,
  allowNonZero: boolean,
): Promise<CLITextResult> {
  try {
    const { stdout, stderr } = await execFileAsync(binaryPath, cliArgs, {
      env,
      cwd,
      timeout: 30_000,
    })
    return { code: 0, stdout, stderr }
  }
  catch (err) {
    const e = err as { stdout?: string, stderr?: string, code?: number | string, message?: string }
    const numericCode = typeof e.code === 'number'
      ? e.code
      : 1
    if (allowNonZero) {
      return { code: numericCode, stdout: e.stdout ?? '', stderr: e.stderr ?? '' }
    }
    throw new Error(`leapmux ${cliArgs.join(' ')} exit=${e.code}: ${e.message}\nstdout: ${e.stdout ?? ''}\nstderr: ${e.stderr ?? ''}`)
  }
}

/** A live streaming or interactive CLI child process. */
export interface LiveCLIProcess {
  child: ReturnType<typeof spawn>
  finished: Promise<{ code: number | null, signal: string | null, error?: string }>
  stdout: () => string
  stderr: () => string
  jsonLines: () => unknown[]
  diagnostic: () => string
  stop: () => Promise<{ code: number | null, signal: string | null, error?: string }>
}

/**
 * Start a streaming or interactive leapmux command (such as `layout watch`,
 * `tunnel forward`, `tunnel connect`, `terminal attach`, or `auth login --device-code`).
 */
export function spawnCLILive(
  cfg: CLIConfigDir | null,
  args: string[],
  options?: { json?: boolean, env?: Record<string, string>, cwd?: string },
): LiveCLIProcess {
  const { binaryPath } = getGlobalState()
  const configDir = cfg?.path ?? options?.env?.LEAPMUX_CONTROL_CONFIG_DIR ?? createTestDirectory('leapmux-cli-live-')
  const env = {
    ...scrubLeapMuxEnv(process.env),
    ...options?.env,
    LEAPMUX_CONTROL_CONFIG_DIR: configDir,
  }
  const cliArgs: string[] = []
  if (cfg)
    cliArgs.push('--hub', cfg.hubURL)
  if (options?.json)
    cliArgs.push('--json')
  cliArgs.push(...args)

  const child = spawn(binaryPath, cliArgs, { env, cwd: options?.cwd })
  let stdoutText = ''
  let stderrText = ''
  child.stdout?.setEncoding('utf8').on('data', (chunk: string) => {
    stdoutText += chunk
  })
  child.stderr?.setEncoding('utf8').on('data', (chunk: string) => {
    stderrText += chunk
  })
  const finished = new Promise<{ code: number | null, signal: string | null, error?: string }>((resolve) => {
    child.once('error', err => resolve({ code: null, signal: null, error: err.message }))
    child.once('close', (code, signal) => resolve({ code, signal }))
  })
  const stop = async () => {
    if (child.exitCode === null && child.signalCode === null) {
      child.stdin?.end()
      child.kill(process.platform === 'win32'
        ? 'SIGTERM'
        : 'SIGINT')
    }
    return finished
  }
  return {
    child,
    finished,
    stdout: () => stdoutText,
    stderr: () => stderrText,
    jsonLines: () => {
      const complete = stdoutText.slice(0, stdoutText.lastIndexOf('\n') + 1).trim()
      if (!complete)
        return []
      return complete.split('\n').filter(Boolean).map(line => parseEnvelope(line, args))
    },
    diagnostic: () => `stdout:\n${stdoutText}\nstderr:\n${stderrText}`,
    stop,
  }
}

interface ControlEventLine {
  kind: string
  tab_id?: string
  sequence?: number
  replayed?: boolean
  body?: string
  request?: string
  request_instance?: string
}

/** Start an events command and keep the complete JSON lines it writes. */
export function startControlEventStream(cli: CLIConfigDir, args: string[]) {
  const command = ['events', ...args]
  const { binaryPath, cliArgs, env } = controlProcess(cli, command)
  const child = spawn(binaryPath, cliArgs, { env })
  let stdout = ''
  let stderr = ''
  child.stdout.setEncoding('utf8').on('data', (text: string) => {
    stdout += text
  })
  child.stderr.setEncoding('utf8').on('data', (text: string) => {
    stderr += text
  })
  const finished = new Promise<{ code: number | null, signal: string | null, error?: string }>((resolve) => {
    child.once('error', (err) => {
      resolve({ code: null, signal: null, error: err.message })
    })
    child.once('close', (code, signal) => {
      resolve({ code, signal })
    })
  })
  const diagnostic = () => `stdout: ${stdout}\nstderr: ${stderr}`
  const events = (): ControlEventLine[] => {
    const lines = stdout.slice(0, stdout.lastIndexOf('\n') + 1).trim().split('\n').filter(Boolean)
    return lines.map(line => parseEnvelope(line, command) as ControlEventLine)
  }
  return { child, finished, diagnostic, events }
}

function controlProcess(cfg: CLIConfigDir, args: string[], overrides?: Record<string, string>) {
  const { binaryPath } = getGlobalState()
  const env = {
    ...scrubLeapMuxEnv(process.env),
    ...overrides,
    LEAPMUX_CONTROL_CONFIG_DIR: cfg.path,
  }
  // The global flags precede the command so they never follow a `--` in args.
  const cliArgs = [
    '--hub',
    cfg.hubURL,
    '--json',
    ...args,
  ]
  return { binaryPath, cliArgs, env }
}

/**
 * Run `leapmux agent open` and return the id of the tab that the
 * hub minted. The CLI envelope is `{"data": ...}` where the payload has
 * snake_case keys including `id`, `workspace_id` and `worker_id`.
 */
export async function cliAgentOpen(cli: CLIConfigDir, params: {
  workspaceId: string
  workerId: string
  provider?: string
}): Promise<string> {
  // Dev-mode workers register every provider they detect on PATH, so
  // `agent open` refuses to guess unless the caller gives one. Default to
  // Claude Code (matches `LEAPMUX_CLAUDE_DEFAULT_MODEL` in the dev fixture)
  // so existing call sites keep working.
  const provider = params.provider ?? 'claude'
  const data = await runCLI(cli, [
    'agent',
    'open',
    '--workspace',
    params.workspaceId,
    '--worker',
    params.workerId,
    '--provider',
    provider,
  ]) as { id?: string } | null
  const id = data?.id
  if (!id || typeof id !== 'string')
    throw new Error(`cliAgentOpen: missing id in response: ${JSON.stringify(data)}`)
  return id
}

export class CLIError extends Error {
  constructor(
    public readonly args: string[],
    public readonly code: string,
    message: string,
    /** The remedies that the CLI printed after the message, one per entry. */
    public readonly hints: string[] = [],
  ) {
    super(`leapmux ${args.join(' ')} failed: ${code}: ${message}`)
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
    throw new Error(`leapmux ${args.join(' ')}: empty stdout`)
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  }
  catch (err) {
    throw new Error(`leapmux ${args.join(' ')}: stdout is not JSON:\n${trimmed}\n\nparse error: ${(err as Error).message}`)
  }
  if (parsed && typeof parsed === 'object') {
    if ('error' in parsed) {
      const e = (parsed as { error: { code?: string, message?: string, hints?: string[] } }).error
      throw new CLIError(args, e.code ?? 'unknown', e.message ?? 'unknown error', e.hints ?? [])
    }
    if ('data' in parsed)
      return (parsed as { data: unknown }).data
  }
  // Some commands stream raw payloads without the data wrapper
  // (e.g. `events` writes JSON-line events). Return as-is for those.
  return parsed
}

/** Write one hub setting through `leapmux admin settings set <key> <value>`. */
export async function setHubSetting(cfg: CLIConfigDir, key: string, value: string): Promise<void> {
  await runCLI(cfg, ['admin', 'settings', 'set', key, value])
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
  const host = url.hostname.toLowerCase()
  if (!host)
    throw new Error(`hubHostForURL: missing hostname in ${hubURL}`)
  const scheme = url.protocol.replace(/:$/, '').toLowerCase() || 'https'
  let key = `${scheme}_${host}`
  if (url.port)
    key = `${key}_${url.port}`
  const cleanPath = url.pathname.replace(/\/+$/, '')
  if (cleanPath) {
    const hash = createHash('sha256').update(cleanPath).digest('hex').slice(0, 12)
    key = `${key}_${hash}`
  }
  return key
}
