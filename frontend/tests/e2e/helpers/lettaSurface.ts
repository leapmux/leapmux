import type { MockModelScriptHost, MockSurface } from './mockModelRequest'
import type { MockQuotaReceipt } from './scenarioQuota'
import { LETTA_CHATGPT_ACCESS_TOKEN, LETTA_CHATGPT_ACCOUNT_ID } from './lettaEnvironment'
import { LETTA_CHATGPT_USAGE_PATH } from './lettaTransport'
import { createScenarioQuota } from './scenarioQuota'

export const LETTA_QUOTA_CONTROL_PREFIX = '/__e2e/letta/quota/'

/** One request that reached the ChatGPT usage service, with the account it named. */
export interface LettaQuotaReceipt extends MockQuotaReceipt {
  accountId: string | undefined
}

/** Serve actual usage HTTP requests; the installed CLI constructs the App Server replies. */
export function createLettaSurface(host: Pick<MockModelScriptHost, 'hasScenario'>): MockSurface {
  const quota = createScenarioQuota<{ accountId: string | undefined }>(host, LETTA_QUOTA_CONTROL_PREFIX, 'ChatGPT account')
  return {
    async handleHttp(request, response, url) {
      if (await quota.handleControl(request, response, url))
        return true
      if (request.method !== 'GET' || url.pathname !== LETTA_CHATGPT_USAGE_PATH)
        return false
      const accountId = typeof request.headers['chatgpt-account-id'] === 'string'
        ? request.headers['chatgpt-account-id']
        : undefined
      const authorized = request.headers.authorization === `Bearer ${LETTA_CHATGPT_ACCESS_TOKEN}` && accountId === LETTA_CHATGPT_ACCOUNT_ID
      quota.answer(response, authorized, { accountId }, 'The request does not use the private ChatGPT account.')
      return true
    },
    clearScenario(scenarioId) {
      quota.clearScenario(scenarioId)
    },
  }
}
