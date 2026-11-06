import type { Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import type { CodexAuthEnvironment } from './codexAuthEnvironment'
import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
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
      // running process rereads nothing, so the backend states the expiry.
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

  it('logout removes the login and login replaces the account', async () => {
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
})

describe('codexAuthGenerationLabel', () => {
  it('reads only a fixture-minted bearer', () => {
    expect(codexAuthGenerationLabel('Bearer e30.eyJnZW5lcmF0aW9uIjoib2xkLTEifQ.fake')).toBe('old-1')
    expect(codexAuthGenerationLabel('Bearer e30.eyJnZW5lcmF0aW9uIjoibmV3In0.fake')).toBe('new')
    expect(codexAuthGenerationLabel('Bearer something-else')).toBeNull()
    expect(codexAuthGenerationLabel(undefined)).toBeNull()
    expect(codexAuthGenerationLabel('Bearer e30.%%%invalid.fake')).toBeNull()
  })
})
