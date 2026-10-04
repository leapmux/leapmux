import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { describe, expect, it, vi } from 'vitest'
import { copilotCatalogMetadata, copilotModelCapabilities, copilotRateLimitHeaders, copilotReasoningFields, handleCopilotHttp } from './copilotSurface'

const options = { sessionToken: 'mock-session', defaultModelId: 'model-default', reasoningModelId: 'model-reasoning' }

function exchange(method: string, pathname: string, host?: string) {
  const request = new IncomingMessage(new Socket())
  request.method = method
  if (host !== undefined)
    request.headers.host = host
  const response = new ServerResponse(request)
  const end = vi.spyOn(response, 'end').mockImplementation(() => response)
  const owned = handleCopilotHttp(request, response, new URL(pathname, 'http://mock.invalid'), options)
  return { owned, response, end }
}

describe('handleCopilotHttp', () => {
  it.each([
    { host: undefined, origin: 'http://127.0.0.1' },
    { host: '127.0.0.1:4422', origin: 'http://127.0.0.1:4422' },
    { host: '[::1]:4422', origin: 'http://[::1]:4422' },
    { host: '', origin: 'http://' },
  ])('keeps the actual Host-derived service origin: $host', ({ host, origin }) => {
    const result = exchange('GET', '/copilot_internal/user', host)
    expect(result.owned).toBe(true)
    expect(result.response.statusCode).toBe(200)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({
      login: 'leapmux-e2e',
      copilot_plan: 'individual_pro',
      token_based_billing: false,
      is_mcp_enabled: false,
      endpoints: { api: origin, telemetry: origin },
      analytics_tracking_id: 'leapmux-e2e',
    })
  })

  it('answers the GitHub identity route without changing its native fields', () => {
    const result = exchange('GET', '/user')
    expect(result.owned).toBe(true)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({ id: 1, login: 'leapmux-e2e', name: 'LeapMux E2E', type: 'User', site_admin: false })
  })

  it('answers automatic selection with the configured model and session token', () => {
    const result = exchange('POST', '/auto')
    expect(result.owned).toBe(true)
    expect(result.response.statusCode).toBe(200)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({
      session_token: options.sessionToken,
      selected_model: { id: 'model-default', name: 'model-default', capabilities: {
        supports: { vision: true, reasoning_effort: ['none', 'low', 'medium', 'high', 'xhigh', 'max'] },
        limits: { max_context_window_tokens: 128_000 },
      } },
    })
  })

  it.each([
    { method: 'POST', path: '/copilot_internal/user' },
    { method: 'POST', path: '/user' },
    { method: 'GET', path: '/auto' },
    { method: 'POST', path: '/auto/' },
    { method: 'GET', path: '/api/v1/userinfo' },
  ])('does not claim $method $path', ({ method, path }) => {
    const result = exchange(method, path)
    expect(result.owned).toBe(false)
    expect(result.end).not.toHaveBeenCalled()
    expect(result.response.headersSent).toBe(false)
  })
})

describe('copilotCatalogMetadata', () => {
  it('keeps the native reasoning-model endpoint and effort vocabulary', () => {
    expect(copilotCatalogMetadata('model-reasoning', options)).toEqual({
      supported_endpoints: ['/responses'],
      capabilities: { supports: { vision: true, reasoning_effort: ['low', 'medium', 'high'] }, limits: { max_context_window_tokens: 128_000 } },
    })
  })

  it.each(['', 'other-model'])('keeps unrelated model capabilities without an effort field: %j', (model) => {
    expect(copilotModelCapabilities(model, options)).toEqual({ supports: { vision: true }, limits: { max_context_window_tokens: 128_000 } })
    expect(copilotCatalogMetadata(model, options)).not.toHaveProperty('supported_endpoints')
  })

  it('returns fresh capability arrays without changing the options', () => {
    const original = JSON.stringify(options)
    const first = copilotModelCapabilities(options.defaultModelId, options)
    const second = copilotModelCapabilities(options.defaultModelId, options)
    expect(first).toEqual(second)
    expect(first.supports).not.toBe(second.supports)
    expect(JSON.stringify(options)).toBe(original)
  })
})

describe('copilotReasoningFields', () => {
  it.each(['', 'Exact reasoning.\n실제 내용', 'x'.repeat(8192)])('preserves the exact reasoning string for its fixture model', (reasoning) => {
    expect(copilotReasoningFields(options.defaultModelId, reasoning, options.defaultModelId)).toEqual({ reasoning })
    expect(copilotReasoningFields('other-model', reasoning, options.defaultModelId)).toEqual({})
  })
})

describe('copilotRateLimitHeaders', () => {
  it('keeps the native entitlement and zero-used reset fields', () => {
    expect(copilotRateLimitHeaders({ type: 'premium_interactions', status: 'allowed', utilization: 0, resetsAt: 0 })).toEqual({
      'x-quota-snapshot-premium_interactions': 'ent=100&rem=100&ov=0&ovPerm=false&rst=1970-01-01T00:00:00.000Z',
    })
  })

  it('preserves native utilization validation and ignores unrelated quota types', () => {
    expect(() => copilotRateLimitHeaders({ type: 'chat', status: 'allowed', utilization: -1 })).toThrow('between zero and one')
    expect(copilotRateLimitHeaders({ type: 'five_hour', status: 'allowed' })).toEqual({})
  })
})
