import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest, expect } from './amp-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 236 — Amp interrupt.
 *
 * Amp takes no interrupt line. The worker sends SIGINT, which makes the CLI cancel the
 * turn on its service, print its cancellation `result`, and EXIT. The turn ends as
 * interrupted, and the next message continues the same thread in a new process
 * (`amp threads continue`). LeapMux pauses the input queue after an interrupt, as it
 * does for every provider.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

/** Press Interrupt, and wait until the turn ends as interrupted. */
async function interruptTurn(page: Page): Promise<void> {
  const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
  await expect(interrupt).toBeVisible()
  await interrupt.click()
  await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
  await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn interrupted/)
}

/**
 * Resume the queue that the interrupt paused, send one more turn, and prove that the
 * agent answers it in the SAME thread: the new process's inference carries the
 * interrupted prompt too.
 */
async function expectThreadContinues(page: Page, modelScript: ModelScript, interruptedPrompt: string): Promise<void> {
  const pause = page.locator('[data-testid="queue-pause-button"]:visible')
  await expect(pause).toHaveText('Resume Queue')
  await pause.click()
  await expect(pause).toHaveText('Pause Queue')
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)
  await expectAssistantAnswer(page)
  const resumed = status.requests.at(-1)
  expect(JSON.stringify(resumed?.body)).toContain(interruptedPrompt)
}

ampTest.describe('Amp interrupt', () => {
  ampTest('stops a model call and continues the thread at the next prompt', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    // A held answer keeps the model call open until the interrupt ends it. The mock
    // counts the step when the inference starts, so the script stays complete
    // although the answer never reaches Amp.
    await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
    await sendMessage(page, modelScript.prompt('Write a long essay about the history of computing.'))
    await modelScript.waitForSteps(1)

    await interruptTurn(page)
    await expectThreadContinues(page, modelScript, 'history of computing')
  })

  ampTest('stops a running command and continues the thread at the next prompt', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    // The command outlives the test by far, so only the interrupt ends it. Amp asks
    // the model nothing after a cancel, so no step follows the call.
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.AMP, 'sleep-call', 'sleep 600')] })
    await sendMessage(page, modelScript.prompt('Wait for ten minutes.'))
    await modelScript.waitForSteps()
    // The command row exists before the interrupt, so the stop ends a command that
    // runs rather than a model call.
    await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: 'sleep 600' }).first()).toBeVisible()

    await interruptTurn(page)
    await expectThreadContinues(page, modelScript, 'Wait for ten minutes.')
  })
})
