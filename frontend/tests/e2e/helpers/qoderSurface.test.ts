import type { Server } from 'node:http'
import { createServer, IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { handleQoderHttp, qoderEndpointCacheRecords } from './qoderSurface'

const origin = 'http://127.0.0.1:4555'
const token = 'isolated-identity-token'
const configServers: Server[] = []

async function configServer(): Promise<string> {
  const server = createServer(async (request, response) => {
    const owned = await handleQoderHttp(request, response, new URL(request.url ?? '/', origin), { origin: () => origin, identityToken: token })
    if (!owned)
      response.writeHead(404).end()
  })
  configServers.push(server)
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolve()
    })
  })
  const address = server.address()
  if (!address || typeof address === 'string')
    throw new Error('The Qoder config fixture has no local TCP address.')
  return `http://127.0.0.1:${address.port}`
}

function exchange(method: string, pathname: string, machineId?: string) {
  const request = new IncomingMessage(new Socket())
  request.method = method
  if (machineId !== undefined)
    request.headers['cosy-machineid'] = machineId
  const response = new ServerResponse(request)
  const end = vi.spyOn(response, 'end').mockImplementation(() => response)
  const owned = handleQoderHttp(request, response, new URL(pathname, 'http://mock.invalid'), { origin: () => origin, identityToken: token })
  return { owned, response, end }
}

afterEach(async () => {
  vi.useRealTimers()
  await Promise.all(configServers.splice(0).map(server => new Promise<void>((resolve, reject) => {
    server.closeAllConnections()
    server.close(error => error ? reject(error) : resolve())
  })))
})

describe('handleQoderHttp', () => {
  it.each([
    { path: '/algo/api/v3/service/region/endpoints', machineId: undefined, node: origin },
    { path: '/algo/api/v5/service/region/endpoints', machineId: undefined, node: { url: origin } },
    { path: '/algo/api/v5/service/region/endpoints', machineId: 'native-machine', node: origin },
    { path: '/algo/api/v5/service/region/endpoints', machineId: '', node: origin },
  ])('keeps the native region response for $path and $machineId', ({ path, machineId, node }) => {
    const result = exchange('GET', path, machineId)
    expect(result.owned).toBe(true)
    expect(result.response.statusCode).toBe(200)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({ centerNodes: [node], inferNodes: [node], security: [node], openapiNodes: [node] })
  })

  it.each(['/api/v1/jobToken/exchange', '/api/v1/jobToken/refresh'])('keeps the isolated token and native millisecond expiry for %s', (path) => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    const result = exchange('POST', path)
    expect(result.owned).toBe(true)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({
      token,
      access_token: token,
      refresh_token: token,
      expires_at: 86_400_000,
      refresh_token_expires_at: 2_592_000_000,
    })
  })

  it('keeps every native user identity field', () => {
    const result = exchange('GET', '/api/v1/userinfo')
    expect(result.owned).toBe(true)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({ id: 'leapmux-e2e', uid: 'leapmux-e2e', username: 'leapmux-e2e', name: 'LeapMux E2E', email: 'e2e@leapmux.test' })
  })

  it('keeps the BYOK permission that the native registration path requires', () => {
    const result = exchange('GET', '/api/v3/user/status')
    expect(result.owned).toBe(true)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({ code: 0, success: true, data: {}, featureSwitches: { allow_byok: 2 } })
  })

  it.each(['/algo/api/v2/model/list', '/api/v2/user/plan', '/ide-text/latest'])('keeps the empty native success envelope for %s', (path) => {
    const result = exchange('GET', path)
    expect(result.owned).toBe(true)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({ code: 0, success: true, data: {} })
  })

  it.each([
    '/algo/api/v3/service/region/endpoints',
    '/algo/api/v5/service/region/endpoints',
    '/api/v3/user/status',
    '/algo/api/v2/model/list',
    '/api/v2/user/plan',
    '/ide-text/latest',
  ])('preserves wildcard methods for %s', (path) => {
    const result = exchange('PATCH', path)
    expect(result.owned).toBe(true)
    expect(result.response.statusCode).toBe(200)
  })

  it.each([
    { method: 'GET', path: '/api/v1/jobToken/exchange' },
    { method: 'GET', path: '/api/v1/jobToken/refresh' },
    { method: 'POST', path: '/api/v1/userinfo' },
    { method: 'GET', path: '/api/v3/user/status/' },
    { method: 'GET', path: '/api/v2/user/plan-extra' },
    { method: 'GET', path: '/v1/models' },
  ])('leaves $method $path untouched', ({ method, path }) => {
    const result = exchange(method, path)
    expect(result.owned).toBe(false)
    expect(result.end).not.toHaveBeenCalled()
    expect(result.response.headersSent).toBe(false)
  })
})

describe('qoderEndpointCacheRecords', () => {
  it.each([0, -1, 1_797_000_000_000, Number.MAX_SAFE_INTEGER])('retains the complete V1 and V2 records with timestamp %s', (updatedAt) => {
    const records = qoderEndpointCacheRecords(origin, updatedAt)
    expect(records.v1).toEqual({ version: 1, entries: { prod: {
      endpoint: origin,
      inferEndpoints: [origin],
      securityEndpoint: origin,
      securityEndpoints: [origin],
      centerEndpoint: origin,
      centerEndpoints: [origin],
      openapiEndpoint: origin,
      openapiEndpoints: [origin],
      updatedAt,
    } } })
    expect(records.v2).toEqual({ version: 2, entries: { prod: { updatedAt, endpointSets: {
      center: { candidates: [origin], selected: origin },
      inference: { candidates: [origin], selected: origin },
      securityInference: { candidates: [origin], selected: origin },
      openapi: { candidates: [origin], selected: origin },
    } } } })
  })

  it('returns independent cache records for separate private origins', () => {
    const first = qoderEndpointCacheRecords(origin, 1)
    const before = JSON.stringify(first)
    const second = qoderEndpointCacheRecords('http://127.0.0.1:4666', 2)
    expect(JSON.stringify(first)).toBe(before)
    expect(second.v1).not.toEqual(first.v1)
    expect(second.v2).not.toEqual(first.v2)
  })
})

describe('handleQoderHttp configuration client', () => {
  const body = { namespaces: ['qodercli-feature-gates'], keys: [], context: { platform: 'macos', clientVersion: '1.1.65', clientType: 'qodercli' } }
  const headers = { 'authorization': `Bearer ${token}`, 'content-type': 'application/json' }

  it('serves the native config namespace and keeps its original prompt policy', async () => {
    const url = await configServer()
    const response = await fetch(`${url}/api/v1/qcs/config/resolve`, { method: 'POST', headers, body: JSON.stringify(body) })
    expect(response.status).toBe(200)
    expect(response.headers.get('etag')).toBe('"leapmux-e2e-qoder-config"')
    expect(await response.json()).toEqual({ configs: { 'qodercli-feature-gates': { prompt_policy: { value: { sectionGates: { 'behavior.act_dont_rederive': 'disable' } }, ruleId: -1, scope: 'default' } } }, etag: 'leapmux-e2e-qoder-config' })
  })

  it('returns an empty unchanged response for the exact native etag', async () => {
    const url = await configServer()
    const response = await fetch(`${url}/api/v1/qcs/config/resolve`, { method: 'POST', headers: { ...headers, 'if-none-match': '"leapmux-e2e-qoder-config"' }, body: JSON.stringify(body) })
    expect(response.status).toBe(304)
    expect(await response.text()).toBe('')
  })

  it.each([
    { request: { ...body, namespaces: ['foreign'] }, label: 'foreign namespace' },
    { request: { ...body, keys: ['foreign'] }, label: 'unknown key' },
    { request: { ...body, context: { ...body.context, clientType: 'foreign' } }, label: 'another native client' },
    { request: {}, label: 'absent fields' },
  ])('rejects $label before returning config values', async ({ request }) => {
    const url = await configServer()
    const response = await fetch(`${url}/api/v1/qcs/config/resolve`, { method: 'POST', headers, body: JSON.stringify(request) })
    expect(response.status).toBe(400)
  })

  it.each(['', 'Bearer real-user-credential'])('refuses an unowned config credential: %j', async (authorization) => {
    const url = await configServer()
    const response = await fetch(`${url}/api/v1/qcs/config/resolve`, { method: 'POST', headers: { ...headers, authorization }, body: JSON.stringify(body) })
    expect(response.status).toBe(401)
  })

  it('rejects malformed JSON and methods outside the native resolve protocol', async () => {
    const url = await configServer()
    const malformed = await fetch(`${url}/api/v1/qcs/config/resolve`, { method: 'POST', headers, body: '{' })
    expect(malformed.status).toBe(400)
    const method = await fetch(`${url}/api/v1/qcs/config/resolve`, { method: 'GET', headers })
    expect(method.status).toBe(405)
  })

  it('opens the native config stream and keeps it open until the client closes', async () => {
    const url = await configServer()
    const controller = new AbortController()
    const response = await fetch(`${url}/api/v1/qcs/config/stream?ns=qodercli-feature-gates`, { headers: { authorization: `Bearer ${token}`, accept: 'text/event-stream' }, signal: controller.signal })
    expect(response.status).toBe(200)
    expect(response.headers.get('content-type')).toContain('text/event-stream')
    const reader = response.body?.getReader()
    if (!reader)
      throw new Error('The native Qoder config stream has no response reader.')
    try {
      const first = await reader.read()
      expect(first.done).toBe(false)
      expect(new TextDecoder().decode(first.value)).toBe('event: connected\ndata: {}\n\n')
    }
    finally {
      await reader.cancel()
      controller.abort()
    }
  })

  it('rejects a foreign config stream namespace', async () => {
    const url = await configServer()
    const response = await fetch(`${url}/api/v1/qcs/config/stream?ns=foreign`, { headers })
    expect(response.status).toBe(400)
  })
})
