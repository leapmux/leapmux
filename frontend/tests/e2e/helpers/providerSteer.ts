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
  const start = await modelScript.queue(
    { gate, toolCalls: [bashToolCall(provider, 'steer-tool', 'printf provider-steer-ready')] },
    { text: 'The turn ended with STEEREDWORD.' },
  )
  try {
    await sendMessage(page, modelScript.prompt('Run the scripted shell command, then reply.'))
    await modelScript.waitForGate(gate)
    await steerQueuedInput(page, { message: steering, match: 'Also include the word' })
    await modelScript.releaseGate(gate)
  }
  finally {
    await modelScript.releaseGateIfHeld(gate)
  }
  await options.approveTool?.(page)

  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  // The request after the tool step carries the steering message that the user inserted.
  const second = await modelScript.requestAt(start + 1)
  expect(JSON.stringify(second.body).includes(steering), 'the steered request holds the inserted message').toBe(true)
  await expectSteeredReply(page, 'STEEREDWORD', 'last')
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(options.resultDividers ?? 1)
}
