import type { MockModelScenarioStatus } from '../helpers/mockModelScript'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { ruleRequest } from '../helpers/mockModelScript'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

/** Send a user turn while Kiro can also start native goal-notification turns. */
export async function sendKiroGoalBarrier(
  context: Pick<NativeScenarioContext, 'page' | 'modelScript'>,
  options: {
    name: string
    prompt: string
    answer: string
    onStage?: (stage: 'model-finished' | 'worker-idle', status: MockModelScenarioStatus) => Promise<void>
  },
) {
  for (const field of ['name', 'prompt', 'answer'] as const) {
    if (options[field].trim() === '')
      throw new Error(`The Kiro goal barrier ${field} must not be empty.`)
  }
  const { page, modelScript } = context
  await modelScript.rule({
    name: options.name,
    when: { protocol: 'aws-event-stream', user: options.prompt },
    respond: { text: options.answer },
    once: true,
  })
  await sendMessage(page, modelScript.prompt(options.prompt))
  const finished = await retryUntilPass(async () => {
    const status = await modelScript.status()
    expect(status.ruleMatches[options.name]).toBe(1)
    expect(ruleRequest(status, options.name).response?.status).toBe(200)
    return status
  })
  await options.onStage?.('model-finished', finished)
  await waitForAgentIdle(page)
  await options.onStage?.('worker-idle', finished)
  const request = ruleRequest(await modelScript.status(), options.name)
  expect(nativeModelContextText(request)).toContain(options.prompt)
  await expect(assistantBubbles(page).filter({ hasText: options.answer }).first()).toBeVisible()
  return request
}
