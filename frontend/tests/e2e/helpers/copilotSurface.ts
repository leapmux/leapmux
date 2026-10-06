import type { IncomingMessage, ServerResponse } from 'node:http'
import type { MockModelRateLimits } from './mockModelScript'
import { writeMockJSON } from './mockHttp'

export interface CopilotSurfaceOptions {
  sessionToken: string
  defaultModelId: string
  reasoningModelId: string
}

/** Preserve the native CAPI capability fields in the shared model catalog. */
export function copilotModelCapabilities(id: string, options: Pick<CopilotSurfaceOptions, 'defaultModelId' | 'reasoningModelId'>): Record<string, unknown> {
  return {
    supports: {
      vision: true,
      ...(id === options.defaultModelId
        ? { reasoning_effort: ['none', 'low', 'medium', 'high', 'xhigh', 'max'] }
        : id === options.reasoningModelId
          ? { reasoning_effort: ['low', 'medium', 'high'] }
          : {}),
    },
    limits: { max_context_window_tokens: 128_000 },
  }
}

/** Return the same native catalog additions for every standard model client. */
export function copilotCatalogMetadata(id: string, options: Pick<CopilotSurfaceOptions, 'defaultModelId' | 'reasoningModelId'>): Record<string, unknown> {
  return {
    ...(id === options.reasoningModelId ? { supported_endpoints: ['/responses'] } : {}),
    capabilities: copilotModelCapabilities(id, options),
  }
}

/** This existing fixture model supplies the extra native Copilot reasoning field. */
export function copilotReasoningFields(model: string, reasoning: string, defaultModelId: string): Record<string, string> {
  return model === defaultModelId ? { reasoning } : {}
}

/** The Copilot quota resources that a native quota snapshot header can state. */
const QUOTA_RESOURCES = new Set(['premium_interactions', 'chat', 'completions'])

/** Encode the native Copilot quota header with a fixed mock entitlement of 100 requests. */
export function copilotRateLimitHeaders(rateLimits: MockModelRateLimits): Record<string, string> {
  if (!QUOTA_RESOURCES.has(rateLimits.type))
    return {}
  const utilization = rateLimits.utilization ?? 0
  if (!Number.isFinite(utilization) || utilization < 0 || utilization > 1)
    throw new Error('The native Copilot quota utilization must be between zero and one.')
  const remaining = Math.round(100 * (1 - utilization))
  const snapshot = new URLSearchParams({ ent: '100', rem: String(remaining), ov: '0', ovPerm: 'false' })
  const resetDate = rateLimits.resetsAt === undefined ? '' : `&rst=${new Date(rateLimits.resetsAt * 1000).toISOString()}`
  return { [`x-quota-snapshot-${rateLimits.type}`]: `${snapshot}${resetDate}` }
}

/** Serve Copilot CAPI and its configured GitHub identity API without consuming script steps. */
export function handleCopilotHttp(request: IncomingMessage, response: ServerResponse, url: URL, options: CopilotSurfaceOptions): boolean {
  if (request.method === 'GET' && url.pathname === '/copilot_internal/user') {
    const requestOrigin = `http://${request.headers.host ?? '127.0.0.1'}`
    writeMockJSON(response, 200, {
      login: 'leapmux-e2e',
      copilot_plan: 'individual_pro',
      token_based_billing: false,
      is_mcp_enabled: false,
      endpoints: { api: requestOrigin, telemetry: requestOrigin },
      analytics_tracking_id: 'leapmux-e2e',
    })
    return true
  }
  if (request.method === 'GET' && url.pathname === '/user') {
    writeMockJSON(response, 200, { id: 1, login: 'leapmux-e2e', name: 'LeapMux E2E', type: 'User', site_admin: false })
    return true
  }
  if (request.method === 'POST' && url.pathname === '/auto') {
    writeMockJSON(response, 200, {
      session_token: options.sessionToken,
      selected_model: { id: options.defaultModelId, name: options.defaultModelId, capabilities: copilotModelCapabilities(options.defaultModelId, options) },
    })
    return true
  }
  return false
}
