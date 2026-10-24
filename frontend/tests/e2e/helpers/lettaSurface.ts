import type { MockModelScriptHost, MockSurface } from './mockModelRequest'
import { isObject } from '../../../src/lib/jsonPick'
import { LETTA_CHATGPT_ACCESS_TOKEN, LETTA_CHATGPT_ACCOUNT_ID } from './lettaEnvironment'
import { LETTA_CHATGPT_USAGE_PATH } from './lettaTransport'
import { readJSONBody, writeMockJSON } from './mockHttp'
import { validateScenarioID } from './mockModelScript'

export const LETTA_QUOTA_CONTROL_PREFIX = '/__e2e/letta/quota/'

export interface LettaQuotaHTTPResponse {
  status: number
  body: unknown
  retryAfterSeconds?: number
}

export interface LettaQuotaReceipt {
  sequence: number
  status: number
  authorized: boolean
  accountId: string | undefined
}

interface QuotaProfile {
  scenarioId: string
  response: LettaQuotaHTTPResponse
  receipts: LettaQuotaReceipt[]
  count: number
}

function parseQuotaResponse(value: unknown): LettaQuotaHTTPResponse {
  if (!isObject(value) || !Number.isInteger(value.status) || typeof value.status !== 'number' || value.status < 200 || value.status > 599 || !Object.hasOwn(value, 'body'))
    throw new Error('A quota response requires an HTTP status from 200 to 599 and a JSON body.')
  if (value.retryAfterSeconds !== undefined && (typeof value.retryAfterSeconds !== 'number' || !Number.isFinite(value.retryAfterSeconds) || value.retryAfterSeconds < 0))
    throw new Error('The quota retry delay must be a non-negative number.')
  return { status: value.status, body: value.body, ...(value.retryAfterSeconds === undefined ? {} : { retryAfterSeconds: value.retryAfterSeconds as number }) }
}

/** Serve actual usage HTTP requests; the installed CLI constructs the App Server replies. */
export function createLettaSurface(host: Pick<MockModelScriptHost, 'hasScenario'>): MockSurface {
  let profile: QuotaProfile | undefined
  return {
    async handleHttp(request, response, url) {
      if (url.pathname.startsWith(LETTA_QUOTA_CONTROL_PREFIX)) {
        try {
          const scenarioId = decodeURIComponent(url.pathname.slice(LETTA_QUOTA_CONTROL_PREFIX.length))
          validateScenarioID(scenarioId)
          if (!host.hasScenario(scenarioId)) {
            writeMockJSON(response, 404, { error: 'The model scenario is not registered.' })
            return true
          }
          if (profile !== undefined && profile.scenarioId !== scenarioId && host.hasScenario(profile.scenarioId)) {
            writeMockJSON(response, 409, { error: 'Another scenario owns the mock ChatGPT account.' })
            return true
          }
          if (request.method === 'POST') {
            const next = parseQuotaResponse(await readJSONBody(request))
            if (profile?.scenarioId === scenarioId)
              profile.response = next
            else
              profile = { scenarioId, response: next, receipts: [], count: 0 }
          }
          else if (request.method !== 'GET') {
            writeMockJSON(response, 405, { error: 'Use GET or POST for quota control.' })
            return true
          }
          if (profile?.scenarioId !== scenarioId)
            writeMockJSON(response, 404, { error: 'The quota response is not configured.' })
          else
            writeMockJSON(response, 200, { scenarioId, receipts: profile.receipts, totalRequests: profile.count })
        }
        catch (error) {
          writeMockJSON(response, 400, { error: error instanceof Error ? error.message : 'Invalid quota control request.' })
        }
        return true
      }
      if (request.method !== 'GET' || url.pathname !== LETTA_CHATGPT_USAGE_PATH)
        return false
      const accountId = typeof request.headers['chatgpt-account-id'] === 'string' ? request.headers['chatgpt-account-id'] : undefined
      const authorized = request.headers.authorization === `Bearer ${LETTA_CHATGPT_ACCESS_TOKEN}` && accountId === LETTA_CHATGPT_ACCOUNT_ID
      const current = profile
      const active = current !== undefined && host.hasScenario(current.scenarioId)
      const status = !authorized ? 401 : active ? current.response.status : 404
      if (active) {
        current.receipts.push({ sequence: ++current.count, status, authorized, accountId })
        if (current.receipts.length > 256)
          current.receipts.splice(0, current.receipts.length - 256)
      }
      if (!authorized)
        writeMockJSON(response, 401, { error: 'The request does not use the private ChatGPT account.' })
      else if (!active)
        writeMockJSON(response, 404, { error: 'No quota response is configured.' })
      else
        writeMockJSON(response, status, current.response.body, current.response.retryAfterSeconds === undefined ? {} : { 'retry-after': String(current.response.retryAfterSeconds) })
      return true
    },
    clearScenario(scenarioId) {
      if (profile?.scenarioId === scenarioId)
        profile = undefined
    },
  }
}
