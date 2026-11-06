import type { IncomingMessage, ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import { Buffer } from 'node:buffer'
import { appendFileSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { join } from 'node:path'
import { Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { finishCleanup, withCleanup } from './cleanup'
import { CODEX_NATIVE_DEFAULT_MODEL, MODEL_KEY } from './mockAgentEnvironment'
import { readMockBody, writeMockJSON } from './mockHttp'

/**
 * A private Codex login fixture for browser scenarios that reproduce managed
 * ChatGPT credential changes with the real Codex CLI.
 *
 * The suite's ordinary Codex fixture disables OpenAI authentication, so it can
 * never observe an expired or replaced login. This fixture gives one Worker's
 * Codex agents a private `CODEX_HOME` whose credentials this test controls:
 *
 * - a local authentication service answers the token endpoint (the refresh
 *   exchange) and the ChatGPT backend paths, and
 * - each authenticated content request is proxied to the scripted mock with the
 *   suite's own mock credential. The fixture records only the generation
 *   label of the bearer token that the request originally held. Request
 *   journals contain no credential values.
 *
 * Codex keeps its credential storage to itself: the fixture writes and removes
 * `auth.json` the way `codex login` and `codex logout` do. All credentials
 * belong to fake accounts that this fixture controls.
 */

/** One fake ChatGPT identity: its account, user, and credential-generation label. */
export interface CodexAuthAccount {
  accountID: string
  userID: string
  /** The label this fixture minted the credentials under, such as `'old'` or `'new'`. */
  generation: string
}

export const CODEX_AUTH_SAME_ACCOUNT: CodexAuthAccount = { accountID: 'auth-same-account', userID: 'auth-same-user', generation: 'old' }
export const CODEX_AUTH_OTHER_ACCOUNT: CodexAuthAccount = { accountID: 'auth-other-account', userID: 'auth-other-user', generation: 'new' }

/** What the token endpoint does with the next refresh exchange. */
type RefreshPolicy
  = | { kind: 'reject' }
    | { kind: 'succeed' }

export interface CodexAuthEnvironment {
  /** The private CODEX_HOME that the worker's Codex agents read. */
  codexHome: string
  /** The origin of the local authentication service. */
  url: string
  /** The journal of the requests the service answered, for diagnostics. */
  journalPath: string
  /** The environment that points one Worker's Codex agents at this fixture. */
  workerEnvironment: Record<string, string>
  /** How many refresh exchanges reached the token endpoint. */
  tokenEndpointCalls: () => number
  /**
   * The generation labels of the content requests the fixture answered, in
   * order. Each label identifies the credential that the running Codex process
   * held; no token value is retained.
   */
  contentGenerations: () => string[]
  /** Make the stored access token expired. The next turn must refresh it. */
  expireAccessToken: () => void
  /** Let the next refresh exchange succeed (it mints a fresh token for the stored login). */
  succeedNextRefresh: () => void
  /** Remove the stored login, as `codex logout` does. */
  logout: () => void
  /** Store a fresh login for account, as `codex login` does. */
  login: (account: CodexAuthAccount) => void
}

export interface CodexAuthEnvironmentOptions {
  /** The base URL of the suite's scripted mock, which ends in `/v1`. */
  mockModelURL: string
  /** The private home directory of the run. */
  homeDir: string
}

/**
 * One private Codex login environment. The fixture cleans its listener and its
 * private files when the scenario ends, on success or failure.
 */
export async function withCodexAuthEnvironment<T>(
  options: CodexAuthEnvironmentOptions,
  use: (environment: CodexAuthEnvironment) => Promise<T>,
): Promise<T> {
  const codexHome = join(options.homeDir, '.codex-auth')
  let refreshPolicy: RefreshPolicy = { kind: 'reject' }
  let tokenEndpointCalls = 0
  const contentGenerations: string[] = []
  let storedAccount: CodexAuthAccount = CODEX_AUTH_SAME_ACCOUNT
  let storedGeneration = ''
  let generationCounter = 0
  // A running process caches credentials and may reload them after a 401.
  // Refuse the expired generation until refresh or login replaces it.
  let storedAccessExpired = false
  // The label of an access token whose expiry the test forced. A model
  // request that carries it answers 401, which is what drives the refresh.
  const EXPIRED_GENERATION = 'expired'

  const issuedCredentials = new Map<string, string>()
  const mint = (account: CodexAuthAccount, generation: string): CodexCredentials => {
    const credentials = mintCredentials(account, generation)
    issuedCredentials.set(credentials.accessToken, generation)
    return credentials
  }
  const activeRequests = new Map<AbortController, Promise<void>>()

  const journalPath = join(codexHome, 'requests.log')
  const journal = (line: string): void => {
    try {
      appendFileSync(journalPath, `${new Date().toISOString()} ${line}\n`)
    }
    catch {
      // The journal is a diagnostic; a failed append stops nothing.
    }
  }
  const server = createServer((request, response) => {
    const controller = new AbortController()
    const disconnect = () => controller.abort()
    request.once('aborted', disconnect)
    response.once('close', disconnect)
    journal(`${request.method} ${request.url ?? '/'} authorization=${codexAuthGenerationLabel(request.headers.authorization) ?? 'none'}`)
    const pending = handleAuthRequest(request, response, {
      mockModelURL: options.mockModelURL,
      mint,
      signal: controller.signal,
      credentialGeneration: (authorization) => {
        const token = /^Bearer\s+(\S+)$/i.exec(authorization ?? '')?.[1]
        return token === undefined
          ? undefined
          : issuedCredentials.get(token)
      },
      currentAccount: () => storedAccount,
      currentGeneration: () => storedGeneration,
      accessExpired: () => storedAccessExpired,
      refreshSucceeded: () => {
        storedAccessExpired = false
      },
      journal,
      refresh: () => {
        tokenEndpointCalls++
        const policy = refreshPolicy
        refreshPolicy = { kind: 'reject' }
        return policy
      },
      recordContent: generation => contentGenerations.push(generation),
    }).finally(() => {
      request.off('aborted', disconnect)
      response.off('close', disconnect)
      activeRequests.delete(controller)
    })
    activeRequests.set(controller, pending)
  })
  return await withCleanup(async () => {
    mkdirSync(codexHome, { recursive: true })
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject)
      server.listen(0, '127.0.0.1', () => {
        server.off('error', reject)
        resolve()
      })
    })
    const address = server.address() as AddressInfo
    const url = `http://127.0.0.1:${address.port}`

    writeFileSync(join(codexHome, 'config.toml'), codexAuthConfig(url), { mode: 0o600 })
    const nextGeneration = (account: CodexAuthAccount): string => {
      generationCounter++
      return `${account.generation}-${generationCounter}`
    }
    const writeAuth = (account: CodexAuthAccount, generation: string) => {
      storedAccount = account
      storedGeneration = generation
      writeFileSync(join(codexHome, 'auth.json'), JSON.stringify(authDocument(mint(account, generation))), { mode: 0o600 })
    }

    const environment: CodexAuthEnvironment = {
      codexHome,
      url,
      journalPath,
      workerEnvironment: {
        CODEX_HOME: codexHome,
        CODEX_REFRESH_TOKEN_URL_OVERRIDE: `${url}/oauth/token`,
      },
      tokenEndpointCalls: () => tokenEndpointCalls,
      contentGenerations: () => [...contentGenerations],
      expireAccessToken: () => {
        storedAccessExpired = true
        // Keep the stored login; only the access token's label states expiry,
        // which the content proxy answers with 401 until a refresh replaces it.
        writeFileSync(join(codexHome, 'auth.json'), JSON.stringify(authDocument(mint(storedAccount, EXPIRED_GENERATION))), { mode: 0o600 })
      },
      succeedNextRefresh: () => {
        refreshPolicy = { kind: 'succeed' }
      },
      logout: () => {
        // A removed login invalidates the stored credential at the backend:
        // every token the running processes still hold is refused from here
        // on, and the account reads answer with no account.
        storedGeneration = ''
        rmSync(join(codexHome, 'auth.json'), { force: true })
      },
      login: (account) => {
        storedAccessExpired = false
        writeAuth(account, nextGeneration(account))
      },
    }
    writeAuth(CODEX_AUTH_SAME_ACCOUNT, nextGeneration(CODEX_AUTH_SAME_ACCOUNT))

    return await use(environment)
  }, async () => {
    for (const controller of activeRequests.keys())
      controller.abort()
    const closed = new Promise<void>((resolve, reject) => {
      if (!server.listening) {
        resolve()
        return
      }
      server.close(error => error
        ? reject(error)
        : resolve())
    })
    server.closeAllConnections()
    await finishCleanup([
      closed,
      ...activeRequests.values(),
      Promise.resolve().then(() => rmSync(codexHome, { recursive: true, force: true })),
    ])
  })
}

/** One fake JWT whose payload the fixture itself minted. Only labels and expiry are real. */
function fakeJWT(payload: Record<string, unknown>): string {
  const encoded = Buffer.from(JSON.stringify(payload), 'utf8').toString('base64url')
  return `e30.${encoded}.fake`
}

/** Read a generation label for diagnostics. This does not validate a credential. */
export function codexAuthGenerationLabel(bearer: string | undefined): string | null {
  if (bearer === undefined)
    return null
  const match = /^Bearer\s+(\S+)$/i.exec(bearer)
  const token = match?.[1]
  if (token === undefined || !token.startsWith('e30.'))
    return null
  try {
    const payload = JSON.parse(Buffer.from(token.split('.')[1]!, 'base64url').toString('utf8'))
    return typeof payload.generation === 'string'
      ? payload.generation
      : null
  }
  catch {
    return null
  }
}

interface CodexCredentials {
  idToken: string
  accessToken: string
  refreshToken: string
  accountID: string
}

function mintCredentials(account: CodexAuthAccount, generation: string): CodexCredentials {
  return {
    idToken: fakeJWT({
      'email': `${account.generation}@example.invalid`,
      'https://api.openai.com/auth': {
        chatgpt_account_id: account.accountID,
        chatgpt_user_id: account.userID,
        chatgpt_plan_type: 'pro',
      },
    }),
    accessToken: fakeJWT({
      generation,
      // A far-future expiry for live labels; the EXPIRED_GENERATION label is
      // the state the content proxy answers with 401, which is what forces a
      // refresh exchange, so its payload expiry never matters.
      exp: generation === 'expired'
        ? 1
        : 4070908800,
    }),
    refreshToken: `${generation}-fake-refresh`,
    accountID: account.accountID,
  }
}

function authDocument(credentials: CodexCredentials): Record<string, unknown> {
  return {
    auth_mode: 'chatgpt',
    tokens: {
      id_token: credentials.idToken,
      access_token: credentials.accessToken,
      refresh_token: credentials.refreshToken,
      account_id: credentials.accountID,
    },
    last_refresh: '2026-01-01T00:00:00Z',
  }
}

function codexAuthConfig(url: string): string {
  return `cli_auth_credentials_store = "file"
check_for_update_on_startup = false
chatgpt_base_url = "${url}"
model_provider = "leapmux-e2e-auth"
model = "${CODEX_NATIVE_DEFAULT_MODEL}"

[memories]
generate_memories = false
use_memories = false
dedicated_tools = false
# Codex supports at least one rollout and at most 48 idle hours here.
# These settings reduce eligible startup work. The fixture answers native
# memory consolidation separately so it consumes no scripted content turn.
max_rollouts_per_startup = 1
min_rollout_idle_hours = 48

[model_providers.leapmux-e2e-auth]
name = "LeapMux E2E auth"
base_url = "${url}/v1"
wire_api = "responses"
requires_openai_auth = true
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
`
}

interface AuthRequestHandling {
  signal: AbortSignal
  credentialGeneration: (authorization: string | undefined) => string | undefined
  mockModelURL: string
  mint: (account: CodexAuthAccount, generation: string) => CodexCredentials
  currentAccount: () => CodexAuthAccount
  currentGeneration: () => string
  accessExpired: () => boolean
  refreshSucceeded: () => void
  journal: (line: string) => void
  refresh: () => RefreshPolicy
  recordContent: (generation: string) => void
}

async function handleAuthRequest(request: IncomingMessage, response: ServerResponse, handling: AuthRequestHandling): Promise<void> {
  try {
    const url = new URL(request.url ?? '/', 'http://auth.invalid')
    if (request.method === 'POST' && url.pathname.endsWith('/oauth/token')) {
      const policy = handling.refresh()
      handling.journal(`decision=refresh policy=${policy.kind}`)
      if (policy.kind === 'reject') {
        // The native permanent rejection: the running process's stored login no
        // longer refreshes. The observed account-switch and revoked-token
        // failures both surface through this answer.
        await readMockBody(request)
        writeMockJSON(response, 401, {
          error: { code: 'refresh_token_invalidated' },
        })
        return
      }
      const credentials = handling.mint(handling.currentAccount(), handling.currentGeneration())
      handling.refreshSucceeded()
      await readMockBody(request)
      writeMockJSON(response, 200, {
        access_token: credentials.accessToken,
        id_token: credentials.idToken,
        refresh_token: credentials.refreshToken,
        token_type: 'bearer',
        expires_in: 3600,
      })
      return
    }
    if ((request.method === 'GET' || request.method === 'POST') && url.pathname.includes('/v1/')) {
      const body = request.method === 'POST'
        ? await readMockBody(request)
        : Buffer.alloc(0)
      if (request.method === 'POST' && isCodexMemoryConsolidation(request, body)) {
        handling.journal('decision=consolidation')
        // Dedicated native housekeeping consumes no scripted content turn.
        respondConsolidation(response)
        return
      }
      const generation = handling.credentialGeneration(request.headers.authorization)
      // Catalog reads produce no turn. Count every content request, including
      // rejected requests, so the scenarios can detect duplicate delivery.
      if (request.method === 'POST')
        handling.recordContent(generation ?? 'unknown')
      if (generation === 'expired' || (generation === handling.currentGeneration() && handling.accessExpired())) {
        handling.journal(`decision=expired generation=${generation}`)
        writeMockJSON(response, 401, {
          error: { code: 'token_expired', message: 'leapmux e2e expired access token' },
        })
        return
      }
      if (generation === undefined || generation !== handling.currentGeneration() || handling.currentGeneration() === '') {
        handling.journal('decision=invalid-credential')
        writeMockJSON(response, 401, {
          error: { code: 'refresh_token_invalidated', message: 'leapmux e2e stale credential' },
        })
        return
      }
      handling.journal(`decision=forward generation=${generation}`)
      await proxyModelRequest(request, response, handling, body)
      return
    }
    if (request.method === 'POST') {
      await readMockBody(request)
      writeMockJSON(response, 200, {})
      return
    }
    // The ChatGPT backend reads of the managed account. The workspace-routing
    // discovery reads the account list here; a login that exists answers it.
    writeMockJSON(response, 200, {
      accounts: handling.currentGeneration() === ''
        ? []
        : [
            {
              id: handling.currentAccount().accountID,
              workspace_backend_origin: 'https://chatgpt.com',
              account_routing_override: 'NO_CONSTRAINT',
            },
          ],
      models: [],
    })
  }
  catch {
    if (response.destroyed)
      return
    if (response.headersSent || handling.signal.aborted) {
      response.destroy()
      return
    }
    writeMockJSON(response, 500, {
      error: { message: 'the codex auth fixture failed' },
    })
  }
}

async function proxyModelRequest(request: IncomingMessage, response: ServerResponse, handling: AuthRequestHandling, body: Buffer): Promise<void> {
  const target = new URL(request.url ?? '/', handling.mockModelURL)
  const headers: Record<string, string> = {}
  for (const [name, value] of Object.entries(request.headers)) {
    if (value === undefined || name === 'host' || name === 'connection' || name === 'content-length')
      continue
    if (name === 'authorization') {
      // The suite mock authenticates its own credential; the fixture's fake
      // login never reaches it.
      headers[name] = `Bearer ${MODEL_KEY}`
      continue
    }
    headers[name] = Array.isArray(value)
      ? value.join(', ')
      : value
  }
  const upstream = await fetch(target, {
    signal: handling.signal,
    method: request.method ?? 'GET',
    headers,
    body: body.length === 0
      ? null
      : new Uint8Array(body),
  })
  const responseHeaders: Record<string, string> = {}
  upstream.headers.forEach((value, name) => {
    if (name !== 'content-encoding' && name !== 'transfer-encoding' && name !== 'content-length')
      responseHeaders[name] = value
  })
  response.writeHead(upstream.status, responseHeaders)
  if (upstream.body === null) {
    response.end()
    return
  }
  await pipeline(Readable.from(upstream.body, { objectMode: false }), response, { signal: handling.signal })
}

/** Whether a model request is Codex's startup memory-consolidation turn. */
function isCodexMemoryConsolidation(request: IncomingMessage, body: Buffer): boolean {
  if (request.headers['x-openai-subagent'] !== 'memory_consolidation' || request.headers['x-openai-memgen-request'] !== 'true')
    return false
  try {
    const parsed = JSON.parse(body.toString('utf8')) as { input?: unknown }
    if (!Array.isArray(parsed.input))
      return false
    for (const item of parsed.input) {
      if (typeof item !== 'object' || item === null)
        continue
      if ((item as { role?: unknown }).role !== 'user')
        continue
      const content = (item as { content?: unknown }).content
      const texts = typeof content === 'string'
        ? [content]
        : Array.isArray(content)
          ? content.filter((part): part is { text?: unknown } => typeof part === 'object' && part !== null).map(part => part.text)
          : []
      if (texts.some(text => typeof text === 'string' && text.startsWith('Consolidate the supplied rollout summaries')))
        return true
    }
    return false
  }
  catch {
    return false
  }
}

/** One completed openai-responses stream with no output, for the consolidation turn. */
function respondConsolidation(response: ServerResponse): void {
  const answer = {
    id: 'resp-auth-fixture',
    object: 'response',
    created_at: 1,
    model: 'memories',
    output: [],
  }
  const events = [
    {
      type: 'response.created',
      response: { ...answer, status: 'in_progress' },
    },
    {
      type: 'response.completed',
      response: {
        ...answer,
        status: 'completed',
        usage: { input_tokens: 1, output_tokens: 0, total_tokens: 1 },
      },
    },
  ]
  const body = events.map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join('')
  response.writeHead(200, { 'content-type': 'text/event-stream' })
  response.end(body)
}
