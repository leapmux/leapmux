import type { IncomingHttpHeaders } from 'node:http'
import type { MockModelCredential } from './mockModelScript'
import { KIRO_E2E_API_KEY, MOCK_COPILOT_GITHUB_TOKEN, MOCK_IDENTITY_TOKEN, MOCK_SESSION_TOKEN, MODEL_KEY } from './mockAgentEnvironment'

const MOCK_CREDENTIALS = new Set([MODEL_KEY, KIRO_E2E_API_KEY, MOCK_COPILOT_GITHUB_TOKEN, MOCK_IDENTITY_TOKEN, MOCK_SESSION_TOKEN])

/** Read actual credential headers and retain only their kind and validation result. */
export function mockCredentialReceipt(headers: IncomingHttpHeaders, queryKeys: readonly string[] = []): MockModelCredential {
  const authorization = headers.authorization
  const apiKeys = [headers['x-api-key'], headers['api-key'], headers['x-goog-api-key'], ...queryKeys].filter(value => value !== undefined)
  if (authorization === undefined && apiKeys.length === 0)
    return { kind: 'none', accepted: false }
  const bearer = typeof authorization === 'string' ? /^Bearer\s+(\S+)$/i.exec(authorization)?.[1] : undefined
  const accepted = queryKeys.length <= 1 && (authorization === undefined || (bearer !== undefined && MOCK_CREDENTIALS.has(bearer)))
    && apiKeys.every(value => typeof value === 'string' && MOCK_CREDENTIALS.has(value))
  return { kind: apiKeys.length > 0 ? 'api-key' : 'bearer', accepted }
}
