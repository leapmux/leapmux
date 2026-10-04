import type { Page } from '@playwright/test'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { bashToolCall } from './providerToolCalls'
import { expectSteeredReply, steerQueuedInput } from './steer'
import { sendMessage, waitForAgentIdle } from './ui'

interface ProviderSteerOptions {
  approveTool?: (page: Page) => Promise<void>
  resultDividers?: number
}

/** Insert a queued message into the native turn before its first tool runs. */
export async function exerciseProviderSteer(page: Page, modelScript: ModelScript, provider: AgentProvider, options: ProviderSteerOptions = {}): Promise<void> {
  const gate = `provider-steer-${provider}`
  const steering = 'Also include the word STEEREDWORD in your reply.'
  try {
    await modelScript.queue(
      { gate, toolCalls: [bashToolCall(provider, 'steer-tool', 'printf provider-steer-ready')] },
      { text: 'The turn ended with STEEREDWORD.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted shell command, then reply.'))
    await modelScript.waitForGate(gate)
    await steerQueuedInput(page, { message: steering, match: 'Also include the word' })
    await modelScript.releaseGate(gate)
  }
  finally {
    await modelScript.releaseGateIfHeld(gate)
  }
  await options.approveTool?.(page)

  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const second = status.requests.find(request => request.stepIndex === 1)
  expect(second, 'the steered turn called the model after its tool').toBeDefined()
  expect(JSON.stringify(second?.body).includes(steering)).toBe(true)
  await expectSteeredReply(page, 'STEEREDWORD', 'last')
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(options.resultDividers ?? 1)
}
