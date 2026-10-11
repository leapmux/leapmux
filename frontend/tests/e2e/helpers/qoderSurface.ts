import type { IncomingMessage, ServerResponse } from 'node:http'
import { isObject } from '../../../src/lib/jsonPick'
import { readJSONBody, writeMockJSON, writeResponseHeaders } from './mockHttp'

export interface QoderSurfaceOptions {
  origin: () => string
  identityToken: string
}

const CONFIG_NAMESPACE = 'qodercli-feature-gates'
const CONFIG_ETAG = 'leapmux-e2e-qoder-config'

async function handleQoderConfiguration(request: IncomingMessage, response: ServerResponse, url: URL, options: QoderSurfaceOptions): Promise<boolean> {
  if (!options.identityToken || request.headers.authorization !== `Bearer ${options.identityToken}`) {
    writeMockJSON(response, 401, { error: 'The Qoder config credential does not belong to this test.' })
    return true
  }
  if (url.pathname.endsWith('/stream')) {
    if (request.method !== 'GET') {
      writeMockJSON(response, 405, { error: 'The Qoder config stream requires GET.' })
      return true
    }
    const namespaces = url.searchParams.getAll('ns')
    if (namespaces.length !== 1 || namespaces[0] !== CONFIG_NAMESPACE) {
      writeMockJSON(response, 400, { error: 'The Qoder config stream requires its native namespace.' })
      return true
    }
    writeResponseHeaders(response, 200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' })
    response.write('event: connected\ndata: {}\n\n')
    return true
  }
  if (request.method !== 'POST') {
    writeMockJSON(response, 405, { error: 'The Qoder config resolver requires POST.' })
    return true
  }
  let body: unknown
  try {
    body = await readJSONBody(request)
  }
  catch (error) {
    writeMockJSON(response, 400, { error: error instanceof Error ? error.message : 'The Qoder config request is invalid.' })
    return true
  }
  if (!isObject(body) || !Array.isArray(body.namespaces) || body.namespaces.length !== 1 || body.namespaces[0] !== CONFIG_NAMESPACE
    || !Array.isArray(body.keys) || body.keys.length !== 0 || !isObject(body.context)
    || body.context.clientType !== 'qodercli' || typeof body.context.clientVersion !== 'string' || !body.context.clientVersion.trim()
    || typeof body.context.platform !== 'string' || !body.context.platform.trim()) {
    writeMockJSON(response, 400, { error: 'The Qoder config request does not match the native client and namespace.' })
    return true
  }
  const etag = `"${CONFIG_ETAG}"`
  if (request.headers['if-none-match'] === etag) {
    writeResponseHeaders(response, 304, { etag })
    response.end()
    return true
  }
  // Keep the native hardcoded policy. The fixture enables no extra feature.
  writeMockJSON(response, 200, {
    configs: { [CONFIG_NAMESPACE]: { prompt_policy: { value: { sectionGates: { 'behavior.act_dont_rederive': 'disable' } }, ruleId: -1, scope: 'default' } } },
    etag: CONFIG_ETAG,
  }, { etag })
  return true
}

/** Keep both endpoint-election cache formats beside the native discovery responses. */
export function qoderEndpointCacheRecords(origin: string, updatedAt: number): { v1: Record<string, unknown>, v2: Record<string, unknown> } {
  const purposes = ['center', 'inference', 'securityInference', 'openapi']
  const endpointSets = Object.fromEntries(purposes.map(purpose => [purpose, { candidates: [origin], selected: origin }]))
  return {
    v2: { version: 2, entries: { prod: { endpointSets, updatedAt } } },
    v1: {
      version: 1,
      entries: {
        prod: {
          endpoint: origin,
          inferEndpoints: [origin],
          securityEndpoint: origin,
          securityEndpoints: [origin],
          centerEndpoint: origin,
          centerEndpoints: [origin],
          openapiEndpoint: origin,
          openapiEndpoints: [origin],
          updatedAt,
        },
      },
    },
  }
}

/** Serve Qoder's native authentication, discovery, and catalog endpoints. */
export function handleQoderHttp(request: IncomingMessage, response: ServerResponse, url: URL, options: QoderSurfaceOptions): boolean | Promise<boolean> {
  if (url.pathname === '/api/v1/qcs/config/resolve' || url.pathname === '/api/v1/qcs/config/stream')
    return handleQoderConfiguration(request, response, url, options)
  if (url.pathname === '/algo/api/v3/service/region/endpoints' || url.pathname === '/algo/api/v5/service/region/endpoints') {
    const origin = options.origin()
    const legacySync = request.headers['cosy-machineid'] !== undefined
    const node = url.pathname.includes('/v5/') && !legacySync ? { url: origin } : origin
    const body = { centerNodes: [node], inferNodes: [node], security: [node], openapiNodes: [node] }
    console.error('[qoder-auth] The mock returned the region endpoints:', JSON.stringify(body))
    writeMockJSON(response, 200, body)
    return true
  }
  if (request.method === 'POST' && (url.pathname === '/api/v1/jobToken/exchange' || url.pathname === '/api/v1/jobToken/refresh')) {
    writeMockJSON(response, 200, {
      token: options.identityToken,
      access_token: options.identityToken,
      refresh_token: options.identityToken,
      expires_at: Date.now() + 86_400_000,
      refresh_token_expires_at: Date.now() + 86_400_000 * 30,
    })
    return true
  }
  if (request.method === 'GET' && url.pathname === '/api/v1/userinfo') {
    writeMockJSON(response, 200, { id: 'leapmux-e2e', uid: 'leapmux-e2e', username: 'leapmux-e2e', name: 'LeapMux E2E', email: 'e2e@leapmux.test' })
    return true
  }
  if (url.pathname === '/api/v3/user/status') {
    writeMockJSON(response, 200, { code: 0, success: true, data: {}, featureSwitches: { allow_byok: 2 } })
    return true
  }
  if (url.pathname === '/algo/api/v2/model/list' || url.pathname === '/api/v2/user/plan' || url.pathname === '/ide-text/latest') {
    writeMockJSON(response, 200, { code: 0, success: true, data: {} })
    return true
  }
  return false
}
