import type { IncomingMessage, ServerResponse } from 'node:http'
import type { MockModelScriptHost } from './mockModelRequest'
import { isObject } from '../../../src/lib/jsonPick'
import { readJSONBody, writeMockJSON } from './mockHttp'
import { validateScenarioID } from './mockModelScript'

/** One scripted answer of a native quota service. */
export interface MockQuotaHTTPResponse {
  status: number
  body: unknown
  retryAfterSeconds?: number
}

/** One request that reached a native quota service while a scenario owned the answer. */
export interface MockQuotaReceipt {
  sequence: number
  status: number
  authorized: boolean
}

/** Parse one scripted answer, as a control request posts it. */
export function parseQuotaResponse(value: unknown): MockQuotaHTTPResponse {
  if (!isObject(value) || !Number.isInteger(value.status) || typeof value.status !== 'number' || value.status < 200 || value.status > 599 || !Object.hasOwn(value, 'body'))
    throw new Error('A quota response requires an HTTP status from 200 to 599 and a JSON body.')
  if (value.retryAfterSeconds !== undefined && (typeof value.retryAfterSeconds !== 'number' || !Number.isFinite(value.retryAfterSeconds) || value.retryAfterSeconds < 0))
    throw new Error('The quota retry delay must be a non-negative number.')
  return { status: value.status, body: value.body, ...(value.retryAfterSeconds === undefined ? {} : { retryAfterSeconds: value.retryAfterSeconds as number }) }
}

/** The quota answer of one native service, and the control that sets it. See {@link createScenarioQuota}. */
export interface ScenarioQuota<Extra extends object> {
  /** Answer a request under the control prefix and return true, or return false for another path. */
  handleControl: (request: IncomingMessage, response: ServerResponse, url: URL) => Promise<boolean>
  /** Answer one request of the native service, and record it while a scenario owns the answer. */
  answer: (response: ServerResponse, authorized: boolean, extra: Extra, unauthorizedMessage: string) => void
  /** Forget the answer of a scenario that the server removed. */
  clearScenario: (scenarioId: string) => void
  /** Whether a registered scenario owns an answer now. */
  active: () => boolean
}

/**
 * The quota answer of one native service, which one model scenario owns.
 *
 * A test posts the answer to `<controlPrefix><scenario>`, and reads the receipts of the requests that reached the
 * service with a GET of the same path. One scenario owns the answer at a time, and removing the scenario forgets it,
 * so no later test reads the quota of an earlier one. The service answers 404 while no scenario owns an answer.
 */
export function createScenarioQuota<Extra extends object>(
  host: Pick<MockModelScriptHost, 'hasScenario'>,
  controlPrefix: string,
  accountLabel: string,
): ScenarioQuota<Extra> {
  const scenarioGenerations = new Map<string, number>()
  let profile: { scenarioId: string, response: MockQuotaHTTPResponse, receipts: (MockQuotaReceipt & Extra)[], count: number } | undefined
  return {
    async handleControl(request, response, url) {
      if (!url.pathname.startsWith(controlPrefix))
        return false
      try {
        const scenarioId = decodeURIComponent(url.pathname.slice(controlPrefix.length))
        validateScenarioID(scenarioId)
        if (!host.hasScenario(scenarioId)) {
          writeMockJSON(response, 404, { error: 'The model scenario is not registered.' })
          return true
        }
        if (profile !== undefined && profile.scenarioId !== scenarioId && host.hasScenario(profile.scenarioId)) {
          writeMockJSON(response, 409, { error: `Another scenario owns the mock ${accountLabel}.` })
          return true
        }
        if (request.method === 'POST') {
          const generation = scenarioGenerations.get(scenarioId) ?? 0
          const body = await readJSONBody(request)
          // The body read yields, so registration and ownership can change before this write.
          if (!host.hasScenario(scenarioId)) {
            writeMockJSON(response, 404, { error: 'The model scenario is not registered.' })
            return true
          }
          if ((scenarioGenerations.get(scenarioId) ?? 0) !== generation) {
            writeMockJSON(response, 409, { error: 'The model scenario changed during quota configuration.' })
            return true
          }
          const next = parseQuotaResponse(body)
          if (profile !== undefined && profile.scenarioId !== scenarioId && host.hasScenario(profile.scenarioId)) {
            writeMockJSON(response, 409, { error: `Another scenario owns the mock ${accountLabel}.` })
            return true
          }
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
    },
    answer(response, authorized, extra, unauthorizedMessage) {
      const current = profile
      const active = current !== undefined && host.hasScenario(current.scenarioId)
      const status = !authorized ? 401 : active ? current.response.status : 404
      if (active) {
        current.receipts.push({ sequence: ++current.count, status, authorized, ...extra })
        if (current.receipts.length > 256)
          current.receipts.splice(0, current.receipts.length - 256)
      }
      if (!authorized)
        writeMockJSON(response, 401, { error: unauthorizedMessage })
      else if (!active)
        writeMockJSON(response, 404, { error: 'No quota response is configured.' })
      else
        writeMockJSON(response, status, current.response.body, current.response.retryAfterSeconds === undefined ? {} : { 'retry-after': String(current.response.retryAfterSeconds) })
    },
    clearScenario(scenarioId) {
      scenarioGenerations.set(scenarioId, (scenarioGenerations.get(scenarioId) ?? 0) + 1)
      if (profile?.scenarioId === scenarioId)
        profile = undefined
    },
    active() {
      return profile !== undefined && host.hasScenario(profile.scenarioId)
    },
  }
}
