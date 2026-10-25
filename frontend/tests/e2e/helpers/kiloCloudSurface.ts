import type { MockModelScriptHost, MockSurface } from './mockModelRequest'
import type { MockQuotaHTTPResponse } from './scenarioQuota'
import { writeMockJSON } from './mockHttp'
import { createScenarioQuota } from './scenarioQuota'

export interface KiloCloudSurfaceOptions {
  /** The access token of the mocked Kilo account, which the suite gives Kilo in `KILO_AUTH_CONTENT`. */
  identityToken: string
}

/**
 * The coding plan of the mocked Kilo account, while a scenario owns its usage. A usage answer names this
 * subscription, because Kilo refuses an answer for another one.
 */
export const KILO_E2E_SUBSCRIPTION = {
  id: 'leapmux-e2e-plan',
  planId: 'leapmux-e2e-lite',
  planName: 'Lite',
  providerName: 'Z.ai',
  providerId: 'zai',
} as const

/** The control route that sets the plan usage of the active scenario. See ./scenarioQuota. */
export const KILO_USAGE_CONTROL_PREFIX = '/__e2e/kilo/usage/'

/** The root of the Kilo cloud procedures that Kilo's provider usage reads, each a tRPC query over GET. */
const TRPC_ROOT = '/api/trpc/'
const PROCEDURE_SUBSCRIPTIONS = 'codingPlans.listSubscriptions'
const PROCEDURE_BYOK = 'byok.list'
const PROCEDURE_USAGE = 'codingPlans.getUsage'

/** One quota window of a coding plan, in the Kilo cloud's own shape. */
export interface KiloQuotaWindow {
  id: string
  remainingPercent: number
  resetsAt: string
  period: { unit: 'hour' | 'day' | 'week' | 'month', value: number }
}

/** Wrap one procedure result as the Kilo cloud answers it. */
export function kiloTrpcResult(data: unknown): { result: { data: { json: unknown } } } {
  return { result: { data: { json: data } } }
}

/** The usage answer of the mocked coding plan, with `windows`. */
export function kiloCodingPlanUsage(windows: readonly KiloQuotaWindow[]): MockQuotaHTTPResponse {
  return {
    status: 200,
    body: kiloTrpcResult({
      schemaVersion: 1,
      fetchedAt: new Date().toISOString(),
      subscription: {
        id: KILO_E2E_SUBSCRIPTION.id,
        planName: KILO_E2E_SUBSCRIPTION.planName,
        providerId: KILO_E2E_SUBSCRIPTION.providerId,
        providerName: KILO_E2E_SUBSCRIPTION.providerName,
        windows,
      },
    }),
  }
}

/**
 * Serve the Kilo cloud procedures that Kilo's provider usage reads: the account's coding plans, its provider keys, and
 * the usage of one plan. The account has its coding plan only while a scenario owns its usage, so every other Kilo
 * agent reads no plan. Each procedure accepts only the mocked account's own credential.
 */
export function createKiloCloudSurface(host: Pick<MockModelScriptHost, 'hasScenario'>, options: KiloCloudSurfaceOptions): MockSurface {
  const usage = createScenarioQuota<Record<string, never>>(host, KILO_USAGE_CONTROL_PREFIX, 'Kilo account')
  return {
    async handleHttp(request, response, url) {
      if (await usage.handleControl(request, response, url))
        return true
      if (!url.pathname.startsWith(TRPC_ROOT))
        return false
      const procedure = url.pathname.slice(TRPC_ROOT.length)
      if (procedure !== PROCEDURE_SUBSCRIPTIONS && procedure !== PROCEDURE_BYOK && procedure !== PROCEDURE_USAGE)
        return false
      if (request.method !== 'GET') {
        writeMockJSON(response, 405, { error: 'A Kilo cloud query requires GET.' })
        return true
      }
      const authorized = !!options.identityToken && request.headers.authorization === `Bearer ${options.identityToken}`
      const unauthorized = 'The Kilo cloud credential does not belong to this test.'
      if (procedure === PROCEDURE_USAGE) {
        if (readInput(url)?.subscriptionId !== KILO_E2E_SUBSCRIPTION.id) {
          writeMockJSON(response, 400, { error: `Kilo asked the usage of another subscription than ${KILO_E2E_SUBSCRIPTION.id}.` })
          return true
        }
        usage.answer(response, authorized, {}, unauthorized)
        return true
      }
      if (!authorized) {
        writeMockJSON(response, 401, { error: unauthorized })
        return true
      }
      const planned = usage.active()
      if (procedure === PROCEDURE_SUBSCRIPTIONS) {
        writeMockJSON(response, 200, kiloTrpcResult(planned
          ? [{ ...KILO_E2E_SUBSCRIPTION, canQueryUsage: true, hasInstalledByokKey: true, status: 'active', cancelAtPeriodEnd: false }]
          : []))
        return true
      }
      writeMockJSON(response, 200, kiloTrpcResult(planned
        ? [{ id: 'leapmux-e2e-key', provider_id: KILO_E2E_SUBSCRIPTION.providerId, management_source: 'coding_plan', is_enabled: true }]
        : []))
      return true
    },
    clearScenario(scenarioId) {
      usage.clearScenario(scenarioId)
    },
  }
}

/** The `input` query parameter of a tRPC query, or undefined when it is absent or not a JSON object. */
function readInput(url: URL): Record<string, unknown> | undefined {
  const raw = url.searchParams.get('input')
  if (raw === null)
    return undefined
  try {
    const value: unknown = JSON.parse(raw)
    return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
  }
  catch {
    return undefined
  }
}
