import type { Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import type { CodexAuthEnvironment } from './codexAuthEnvironment'
import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import * as fs from 'node:fs'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import * as http from 'node:http'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setImmediate } from 'node:timers/promises'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import {
  CODEX_AUTH_OTHER_ACCOUNT,
  CODEX_AUTH_SAME_ACCOUNT,

  codexAuthGenerationLabel,
  withCodexAuthEnvironment,
} from './codexAuthEnvironment'

/**
 * The Codex auth fixture's own behavior: credential-generation changes, the
 * token-endpoint answers, content forwarding to the suite mock, and cleanup.
 * No real browser login or account takes part.
 */

let upstream: Server
let upstreamURL: string
const upstreamRequests: { path: string, authorization: string, body: string }[] = []
const CONNECTION_CLOSED_CODES = /^(?:UND_ERR_SOCKET|ECONNRESET)$/

beforeAll(async () => {
  upstream = createServer((request, response) => {
    const chunks: Buffer[] = []
    request.on('data', chunk => chunks.push(chunk))
    request.on('end', () => {
      upstreamRequests.push({
        path: request.url ?? '/',
        authorization: String(request.headers.authorization ?? ''),
        body: Buffer.concat(chunks).toString('utf8'),
      })
      const body = JSON.stringify({ ok: true, path: request.url })
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end(body)
    })
  })
  await new Promise<void>(resolve => upstream.listen(0, '127.0.0.1', resolve))
  upstreamURL = `http://127.0.0.1:${(upstream.address() as AddressInfo).port}/v1`
})

afterAll(async () => {
  await new Promise<void>(resolve => upstream.close(() => resolve()))
})

function post(environment: CodexAuthEnvironment, path: string, body: string, authorization?: string): Promise<Response> {
  return fetch(`${environment.url}${path}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      ...(authorization === undefined
        ? {}
        : { authorization }),
    },
    body,
  })
}

function interceptUpstream(handler: (init: RequestInit | undefined) => Promise<Response>) {
  const nativeFetch = globalThis.fetch
  return vi.spyOn(globalThis, 'fetch').mockImplementation((input, init) => String(input).startsWith(upstreamURL)
    ? handler(init)
    : nativeFetch(input, init))
}

/** The stored access token of the fixture's current login. */
function storedBearer(environment: CodexAuthEnvironment): string {
  const auth = JSON.parse(readFileSync(join(environment.codexHome, 'auth.json'), 'utf8')) as { tokens: { access_token: string } }
  return `Bearer ${auth.tokens.access_token}`
}

/** A unique private home per case, so cases share no codex home. */
function uniqueHomeDir(): string {
  return mkdtempSync(join(tmpdir(), `codex-auth-fixture-${randomUUID().slice(0, 8)}-`))
}

describe('withCodexAuthEnvironment', () => {
  it('writes the private codex home, the worker environment, and an initial login', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      expect(existsSync(join(environment.codexHome, 'config.toml'))).toBe(true)
      const config = readFileSync(join(environment.codexHome, 'config.toml'), 'utf8')
      expect(config).toContain(`chatgpt_base_url = "${environment.url}"`)
      expect(config).toContain(`base_url = "${environment.url}/v1"`)
      expect(config).toContain('requires_openai_auth = true')
      expect(config).toContain('cli_auth_credentials_store = "file"')
      expect(config).toContain('max_rollouts_per_startup = 1')
      expect(config).toContain('min_rollout_idle_hours = 48')
      expect(environment.workerEnvironment.CODEX_HOME).toBe(environment.codexHome)
      expect(environment.workerEnvironment.CODEX_REFRESH_TOKEN_URL_OVERRIDE).toBe(`${environment.url}/oauth/token`)
      const auth = JSON.parse(readFileSync(join(environment.codexHome, 'auth.json'), 'utf8')) as { tokens: { account_id: string } }
      expect(auth.tokens.account_id).toBe(CODEX_AUTH_SAME_ACCOUNT.accountID)
      expect(codexAuthGenerationLabel(storedBearer(environment))).toMatch(/^old-\d+$/)
    })
  })

  it('rejects a refresh exchange by default and succeeds once after succeedNextRefresh', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      const rejected = await post(environment, '/oauth/token', 'grant_type=refresh_token')
      expect(rejected.status).toBe(401)
      await expect(rejected.json()).resolves.toEqual({
        error: { code: 'refresh_token_invalidated' },
      })
      expect(environment.tokenEndpointCalls()).toBe(1)

      environment.succeedNextRefresh()
      const accepted = await post(environment, '/oauth/token', 'grant_type=refresh_token')
      expect(accepted.status).toBe(200)
      const tokens = await accepted.json() as { access_token: string, token_type: string }
      expect(tokens.token_type).toBe('bearer')
      expect(codexAuthGenerationLabel(`Bearer ${tokens.access_token}`)).toMatch(/^old-\d+$/)

      const rejectedAgain = await post(environment, '/oauth/token', 'grant_type=refresh_token')
      expect(rejectedAgain.status).toBe(401)
      expect(environment.tokenEndpointCalls()).toBe(3)
    })
  })

  it('answers an expired access token with 401 and forwards the current credential', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      // The bearer a running process still holds from before the expiry.
      const cached = storedBearer(environment)
      environment.expireAccessToken()
      expect(codexAuthGenerationLabel(storedBearer(environment))).toBe('expired')
      const expired = await post(environment, '/v1/responses', '{}', storedBearer(environment))
      expect(expired.status).toBe(401)
      await expect(expired.json()).resolves.toEqual({
        error: { code: 'token_expired', message: 'leapmux e2e expired access token' },
      })
      // The cached credential of the pre-expiry login is refused as well: a
      // process may reload credentials after a 401, so the backend states expiry.
      const refused = await post(environment, '/v1/responses', '{}', cached)
      expect(refused.status).toBe(401)

      // A fresh login of the same account forwards to the suite mock, which
      // reads the suite credential: the fixture's fake login never reaches it.
      environment.login(CODEX_AUTH_SAME_ACCOUNT)
      const before = upstreamRequests.length
      const forwarded = await post(environment, '/v1/responses', '{"stream":true}', storedBearer(environment))
      expect(forwarded.status).toBe(200)
      await expect(forwarded.json()).resolves.toEqual({ ok: true, path: '/v1/responses' })
      // Every model request counts, rejected or forwarded: the expired
      // label, the refused cached credential, and the forwarded fresh login.
      expect(environment.contentGenerations()).toEqual([
        'expired',
        'old-1',
        expect.stringMatching(/^old-\d+$/),
      ] as unknown as string[])
      expect(upstreamRequests.length).toBe(before + 1)
      expect(upstreamRequests.at(-1)?.authorization).toBe('Bearer leapmux-e2e-model-key')
      expect(upstreamRequests.at(-1)?.body).toBe('{"stream":true}')
    })
  })

  it('rejects the credential of a login that moved on', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      const stale = storedBearer(environment)
      environment.login(CODEX_AUTH_OTHER_ACCOUNT)
      const staleRequest = await post(environment, '/v1/responses', '{}', stale)
      expect(staleRequest.status).toBe(401)
      await expect(staleRequest.json()).resolves.toEqual({
        error: { code: 'refresh_token_invalidated', message: 'leapmux e2e stale credential' },
      })

      const freshRequest = await post(environment, '/v1/responses', '{}', storedBearer(environment))
      expect(freshRequest.status).toBe(200)
    })
  })

  it('answers the backend reads and the unrelated posts', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      const check = await fetch(`${environment.url}/api/codex/accounts/check`)
      expect(check.status).toBe(200)
      const accounts = await check.json() as { accounts: { id: string }[] }
      expect(accounts.accounts[0]?.id).toBe(CODEX_AUTH_SAME_ACCOUNT.accountID)

      const unrelated = await post(environment, '/backend/something', '{}')
      expect(unrelated.status).toBe(200)
      await expect(unrelated.json()).resolves.toEqual({})
    })
  })

  it('removes the login on logout and replaces the account on login', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      environment.login(CODEX_AUTH_OTHER_ACCOUNT)
      const switched = JSON.parse(readFileSync(join(environment.codexHome, 'auth.json'), 'utf8')) as { tokens: { account_id: string } }
      expect(switched.tokens.account_id).toBe(CODEX_AUTH_OTHER_ACCOUNT.accountID)

      const cached = storedBearer(environment)
      environment.logout()
      expect(existsSync(join(environment.codexHome, 'auth.json'))).toBe(false)
      // A removed login refuses the credential the processes still hold, and
      // the account reads answer with no account.
      const refused = await post(environment, '/v1/responses', '{}', cached)
      expect(refused.status).toBe(401)
      const accounts = await fetch(`${environment.url}/api/codex/accounts/check`)
      await expect(accounts.json()).resolves.toEqual({ accounts: [], models: [] })
    })
  })

  it('cleans its listener and private files on failure', async () => {
    let codexHome = ''
    await expect(withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      codexHome = environment.codexHome
      throw new Error('scenario failed')
    })).rejects.toThrow('scenario failed')
    expect(existsSync(codexHome)).toBe(false)
  })

  it('cleans partial initialization when writing the configuration fails', async () => {
    const homeDir = uniqueHomeDir()
    const originalListen = http.Server.prototype.listen
    const listeners: Server[] = []
    const listen = vi.spyOn(http.Server.prototype, 'listen').mockImplementation(function (this: Server, ...args: Parameters<Server['listen']>) {
      listeners.push(this)
      fs.mkdirSync(join(homeDir, '.codex-auth', 'config.toml'), { recursive: true })
      return originalListen.apply(this, args)
    })
    try {
      await expect(withCodexAuthEnvironment({ mockModelURL: upstreamURL, homeDir }, async () => {})).rejects.toMatchObject({
        code: expect.stringMatching(/^(?:EISDIR|EPERM)$/),
        path: join(homeDir, '.codex-auth', 'config.toml'),
        syscall: 'open',
      })
      expect(listeners[0]?.listening).toBe(false)
      expect(existsSync(join(homeDir, '.codex-auth'))).toBe(false)
    }
    finally {
      listen.mockRestore()
      const listener = listeners[0]
      listener?.closeAllConnections()
      if (listener?.listening)
        await new Promise<void>(resolve => listener.close(() => resolve()))
      fs.rmSync(homeDir, { recursive: true, force: true })
    }
  })

  it('removes partial files when the listener cannot start', async () => {
    const homeDir = uniqueHomeDir()
    const failure = new Error('listener failed')
    const entered = deferred<Server>()
    const listen = vi.spyOn(http.Server.prototype, 'listen').mockImplementation(function (this: Server) {
      entered.resolve(this)
      return this
    })
    const operation = withCodexAuthEnvironment({ mockModelURL: upstreamURL, homeDir }, async () => {})
    void operation.catch(() => {})
    try {
      const listener = await entered.promise
      expect(listener.listenerCount('error')).toBeGreaterThan(0)
      listener.emit('error', failure)
      await expect(operation).rejects.toThrow(failure)
      expect(existsSync(join(homeDir, '.codex-auth'))).toBe(false)
    }
    finally {
      listen.mockRestore()
      fs.rmSync(homeDir, { recursive: true, force: true })
    }
  })

  it('aborts an upstream request when the client disconnects before headers', async () => {
    let upstreamSignal: AbortSignal | undefined
    const started = deferred<void>()
    const aborted = deferred<void>()
    const mockedFetch = interceptUpstream((init) => {
      upstreamSignal = init?.signal ?? undefined
      started.resolve()
      return new Promise<Response>((_resolve, reject) => {
        upstreamSignal?.addEventListener('abort', () => {
          aborted.resolve()
          reject(upstreamSignal?.reason)
        }, { once: true })
      })
    })
    try {
      await withCodexAuthEnvironment({
        mockModelURL: upstreamURL,
        homeDir: uniqueHomeDir(),
      }, async (environment) => {
        const controller = new AbortController()
        const client = fetch(`${environment.url}/v1/responses`, {
          method: 'POST',
          body: '{}',
          headers: {
            authorization: storedBearer(environment),
          },
          signal: controller.signal,
        })
        void client.catch(() => {})
        await started.promise
        const reason = new Error('the client ended the authentication fixture request')
        controller.abort(reason)
        await expect(client).rejects.toBe(reason)
        expect(upstreamSignal).toBeDefined()
        await aborted.promise
        expect(upstreamSignal?.aborted).toBe(true)
      })
    }
    finally {
      mockedFetch.mockRestore()
    }
  })

  it('aborts unfinished upstream work when the fixture closes', async () => {
    const started = deferred<void>()
    let upstreamSignal: AbortSignal | undefined
    const mockedFetch = interceptUpstream((init) => {
      upstreamSignal = init?.signal ?? undefined
      started.resolve()
      return new Promise<Response>((_resolve, reject) => {
        upstreamSignal?.addEventListener('abort', () => reject(upstreamSignal?.reason), { once: true })
      })
    })
    let client: Promise<Response> | undefined
    try {
      await withCodexAuthEnvironment({
        mockModelURL: upstreamURL,
        homeDir: uniqueHomeDir(),
      }, async (environment) => {
        client = post(environment, '/v1/responses', '{}', storedBearer(environment))
        void client.catch(() => {})
        await started.promise
      })
      expect(upstreamSignal?.aborted).toBe(true)
      await expect(client).rejects.toMatchObject({
        name: 'TypeError',
        cause: {
          code: expect.stringMatching(CONNECTION_CLOSED_CODES),
        },
      })
    }
    finally {
      mockedFetch.mockRestore()
    }
  })

  it('cleans an incomplete request body without forwarding content', async () => {
    const requestStarted = deferred<void>()
    const clientClosed = deferred<void>()
    const originalListen = http.Server.prototype.listen
    const listen = vi.spyOn(http.Server.prototype, 'listen').mockImplementation(function (this: Server, ...args: Parameters<Server['listen']>) {
      this.once('request', () => requestStarted.resolve())
      return originalListen.apply(this, args)
    })
    let client: http.ClientRequest | undefined
    let codexHome = ''
    const before = upstreamRequests.length
    try {
      await withCodexAuthEnvironment({
        mockModelURL: upstreamURL,
        homeDir: uniqueHomeDir(),
      }, async (environment) => {
        codexHome = environment.codexHome
        client = http.request(`${environment.url}/v1/responses`, {
          method: 'POST',
          headers: {
            authorization: storedBearer(environment),
          },
        })
        client.on('error', () => {})
        client.once('close', () => clientClosed.resolve())
        client.write('{')
        await requestStarted.promise
      })
      expect(existsSync(codexHome)).toBe(false)
      await clientClosed.promise
      expect(upstreamRequests).toHaveLength(before)
    }
    finally {
      client?.destroy()
      listen.mockRestore()
    }
  })

  it('closes a response when its upstream stream fails after headers', async () => {
    const originalWriteHead = http.ServerResponse.prototype.writeHead
    let repeatedHeaders = 0
    const writeHead = vi.spyOn(http.ServerResponse.prototype, 'writeHead').mockImplementation(function (this: http.ServerResponse, ...args: Parameters<http.ServerResponse['writeHead']>) {
      if (this.headersSent) {
        repeatedHeaders++
        this.destroy()
        return this
      }
      return originalWriteHead.apply(this, args)
    })
    const stream = deferred<ReadableStreamDefaultController<Uint8Array>>()
    const mockedFetch = interceptUpstream(() => {
      return Promise.resolve(new Response(new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(new TextEncoder().encode('first chunk'))
          stream.resolve(controller)
        },
      })))
    })
    try {
      await withCodexAuthEnvironment({
        mockModelURL: upstreamURL,
        homeDir: uniqueHomeDir(),
      }, async (environment) => {
        const response = await post(environment, '/v1/responses', '{}', storedBearer(environment))
        const reader = response.body!.getReader()
        expect(new TextDecoder().decode((await reader.read()).value)).toBe('first chunk')
        const remainder = reader.read()
        ;(await stream.promise).error(new Error('upstream failed after headers'))
        await expect(remainder).rejects.toMatchObject({
          name: 'TypeError',
          message: 'terminated',
          cause: {
            code: expect.stringMatching(CONNECTION_CLOSED_CODES),
          },
        })
        expect(repeatedHeaders).toBe(0)
      })
    }
    finally {
      mockedFetch.mockRestore()
      writeHead.mockRestore()
    }
  })

  it('waits for downstream drain before writing the next upstream chunk', async () => {
    const firstWrite = deferred<void>()
    const originalWrite = http.ServerResponse.prototype.write
    const downstreamResponses: http.ServerResponse[] = []
    let heldChunk: unknown
    let writes = 0
    let released = false
    const write = vi.spyOn(http.ServerResponse.prototype, 'write').mockImplementation(function (this: http.ServerResponse, ...args: Parameters<http.ServerResponse['write']>) {
      writes++
      if (writes === 1) {
        downstreamResponses.push(this)
        heldChunk = args[0]
        firstWrite.resolve()
        return false
      }
      return originalWrite.apply(this, args)
    })
    const mockedFetch = interceptUpstream(() => {
      let index = 0
      return Promise.resolve(new Response(new ReadableStream<Uint8Array>({
        pull(controller) {
          if (index < 3)
            controller.enqueue(new TextEncoder().encode(String(++index)))
          else
            controller.close()
        },
      })))
    })
    const release = () => {
      const downstream = downstreamResponses[0]
      if (released || downstream === undefined)
        return
      released = true
      if (!downstream.writableEnded)
        originalWrite.call(downstream, heldChunk as Uint8Array, 'utf8')
      downstream.emit('drain')
    }
    try {
      await withCodexAuthEnvironment({
        mockModelURL: upstreamURL,
        homeDir: uniqueHomeDir(),
      }, async (environment) => {
        const client = post(environment, '/v1/responses', '{}', storedBearer(environment))
        void client.catch(() => {})
        await firstWrite.promise
        await setImmediate()
        try {
          expect(writes).toBe(1)
        }
        finally {
          release()
        }
        expect(await (await client).text()).toBe('123')
      })
    }
    finally {
      release()
      write.mockRestore()
      mockedFetch.mockRestore()
    }
  })

  it('cleans its listener and private files on success', async () => {
    let codexHome = ''
    let url = ''
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      codexHome = environment.codexHome
      url = environment.url
    })
    expect(existsSync(codexHome)).toBe(false)
    await expect(fetch(url)).rejects.toMatchObject({
      name: 'TypeError',
      cause: { code: 'ECONNREFUSED' },
    })
  })

  it('refuses missing and unrecognized credentials before forwarding content or catalog requests', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      const before = upstreamRequests.length
      for (const authorization of [
        undefined,
        'Bearer not-a-managed-login',
        'Basic arbitrary',
        'Bearer e30.eyJnZW5lcmF0aW9uIjoib2xkLTEifQ.forged',
      ]) {
        const content = await post(environment, '/v1/responses', '{}', authorization)
        expect(content.status).toBe(401)
        const catalog = await fetch(`${environment.url}/v1/models`, {
          headers: authorization === undefined
            ? {}
            : { authorization },
        })
        expect(catalog.status).toBe(401)
      }
      expect(upstreamRequests).toHaveLength(before)
    })
  })

  it('validates current, expired, stale and removed catalog credentials without counting content', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      const readCatalog = (authorization: string) => fetch(`${environment.url}/v1/models`, {
        headers: { authorization },
      })
      const current = storedBearer(environment)
      expect((await readCatalog(current)).status).toBe(200)
      environment.expireAccessToken()
      expect((await readCatalog(current)).status).toBe(401)
      expect((await readCatalog(storedBearer(environment))).status).toBe(401)
      environment.login(CODEX_AUTH_OTHER_ACCOUNT)
      expect((await readCatalog(current)).status).toBe(401)
      const fresh = storedBearer(environment)
      expect((await readCatalog(fresh)).status).toBe(200)
      environment.logout()
      expect((await readCatalog(fresh)).status).toBe(401)
      expect(environment.contentGenerations()).toEqual([])
    })
  })

  it.each([
    {
      role: 'user',
      headers: { 'x-openai-subagent': 'memory_consolidation' },
    },
    {
      role: 'user',
      headers: { 'x-openai-memgen-request': 'true' },
    },
    {
      role: 'assistant',
      headers: { 'x-openai-subagent': 'memory_consolidation', 'x-openai-memgen-request': 'true' },
    },
  ])('forwards content with incomplete consolidation identity: %j', async ({ role, headers }) => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      const answer = await fetch(`${environment.url}/v1/responses`, {
        method: 'POST',
        headers: {
          ...headers,
          authorization: storedBearer(environment),
        },
        body: JSON.stringify({
          input: [
            { role, content: 'Consolidate the supplied rollout summaries into memories' },
          ],
        }),
      })
      await expect(answer.json()).resolves.toEqual({ ok: true, path: '/v1/responses' })
      expect(environment.contentGenerations()).toEqual(['old-1'])
    })
  })

  it('forwards ordinary and historical messages that resemble memory consolidation', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      const before = upstreamRequests.length
      for (const role of ['user', 'assistant']) {
        const body = JSON.stringify({
          input: [
            { role, content: 'Consolidate the supplied rollout summaries for my report' },
          ],
        })
        const answer = await post(environment, '/v1/responses', body, storedBearer(environment))
        await expect(answer.json()).resolves.toEqual({ ok: true, path: '/v1/responses' })
      }
      expect(upstreamRequests).toHaveLength(before + 2)
      expect(environment.contentGenerations()).toEqual(['old-1', 'old-1'])
    })
  })

  it('answers dedicated native memory consolidation without consuming a scripted content turn', async () => {
    await withCodexAuthEnvironment({
      mockModelURL: upstreamURL,
      homeDir: uniqueHomeDir(),
    }, async (environment) => {
      const before = upstreamRequests.length
      environment.logout()
      const response = await fetch(`${environment.url}/v1/responses`, {
        method: 'POST',
        headers: {
          'x-openai-subagent': 'memory_consolidation',
          'x-openai-memgen-request': 'true',
        },
        body: JSON.stringify({
          input: [
            {
              role: 'user',
              content: [
                { type: 'input_text', text: 'Consolidate the supplied rollout summaries into memories' },
              ],
            },
          ],
        }),
      })
      expect(response.status).toBe(200)
      expect(await response.text()).toContain('response.completed')
      expect(upstreamRequests).toHaveLength(before)
      expect(environment.contentGenerations()).toEqual([])
    })
  })
})

describe('codexAuthGenerationLabel', () => {
  it('reads diagnostic generation labels from bearer payloads', () => {
    expect(codexAuthGenerationLabel('Bearer e30.eyJnZW5lcmF0aW9uIjoib2xkLTEifQ.fake')).toBe('old-1')
    expect(codexAuthGenerationLabel('Bearer e30.eyJnZW5lcmF0aW9uIjoibmV3In0.fake')).toBe('new')
    expect(codexAuthGenerationLabel('Bearer something-else')).toBeNull()
    expect(codexAuthGenerationLabel(undefined)).toBeNull()
    expect(codexAuthGenerationLabel('Bearer e30.%%%invalid.fake')).toBeNull()
  })
})
