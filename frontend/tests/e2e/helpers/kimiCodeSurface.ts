import type { MockModelScriptHost, MockSurface } from './mockModelRequest'
import type { MockQuotaHTTPResponse } from './scenarioQuota'
import { writeMockJSON } from './mockHttp'
import { createScenarioQuota } from './scenarioQuota'

export interface KimiCodeSurfaceOptions {
  /** The access token of the mocked Kimi Code account, which the suite stores in Kimi's credential file. */
  accessToken: string
}

/**
 * The root of the mocked Kimi Code service, which stands in for `https://api.kimi.com/coding/v1`. Kimi Code reads
 * the root from `KIMI_CODE_BASE_URL` (./kimiEnvironment).
 */
export const KIMI_CODE_SERVICE_PATH = '/kimi/coding/v1'

/** The control route that sets the plan usage of the active scenario. See ./scenarioQuota. */
export const KIMI_USAGE_CONTROL_PREFIX = '/__e2e/kimi/usage/'

/** The route that reports the plan usage. Kimi Code reads it for `GET /api/v1/oauth/usage`. */
const USAGE_PATH = `${KIMI_CODE_SERVICE_PATH}/usages`

/** One quota window of the plan, in the Kimi Code service's own shape: the used share, where 1 is the whole window. */
export interface KimiQuotaWindow {
  used_ratio: number
  reset_time?: string
}

/** The quota windows of the plan, by the Kimi Code service's own names. */
export interface KimiPlanWindows {
  limit_5h?: KimiQuotaWindow
  limit_7d?: KimiQuotaWindow
  limit_month_total?: KimiQuotaWindow
  /** The share of the monthly window that Kimi Code itself used. It is part of `limit_month_total`. */
  limit_month_code?: KimiQuotaWindow
}

/** The usage answer of the mocked plan, with `windows`. */
export function kimiPlanUsage(windows: KimiPlanWindows): MockQuotaHTTPResponse {
  return { status: 200, body: { usages: windows } }
}

/**
 * Serve the plan usage of the mocked Kimi Code account. The plan reports its usage only while a scenario owns it, so
 * every other Kimi agent that reads the usage gets 404, which Kimi Code reports as a failed read. The route accepts
 * only the mocked account's own credential.
 */
export function createKimiCodeSurface(host: Pick<MockModelScriptHost, 'hasScenario'>, options: KimiCodeSurfaceOptions): MockSurface {
  const usage = createScenarioQuota<Record<string, never>>(host, KIMI_USAGE_CONTROL_PREFIX, 'Kimi Code account')
  return {
    async handleHttp(request, response, url) {
      if (await usage.handleControl(request, response, url))
        return true
      if (!url.pathname.startsWith(`${KIMI_CODE_SERVICE_PATH}/`))
        return false
      if (url.pathname !== USAGE_PATH) {
        writeMockJSON(response, 404, { error: `The mocked Kimi Code service serves only ${USAGE_PATH}.` })
        return true
      }
      if (request.method !== 'GET') {
        writeMockJSON(response, 405, { error: 'The Kimi Code usage requires GET.' })
        return true
      }
      const authorized = !!options.accessToken && request.headers.authorization === `Bearer ${options.accessToken}`
      usage.answer(response, authorized, {}, 'The Kimi Code credential does not belong to this test.')
      return true
    },
    clearScenario(scenarioId) {
      usage.clearScenario(scenarioId)
    },
  }
}
