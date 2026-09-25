import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'
import { expect, OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

/**
 * 139 — Oh My Pi interrupt.
 *
 * The interrupt button sends omp's `abort` command. omp stops the model call or
 * the running tool, and ends the run with an `agent_end` whose last assistant
 * message states `aborted`. LeapMux then pauses the input queue, as it does for
 * every provider, and the agent takes the next prompt once the reader resumes it.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

/**
 * Press Interrupt, and wait until the turn ends as interrupted.
 *
 * `divider` is the whole text of the turn-end divider. The worker measures the
 * turn, so the divider states a time after the words, and a turn that called a
 * tool states the count after that.
 */
async function interruptTurn(page: Page, divider: RegExp): Promise<void> {
  const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
  await expect(interrupt).toBeVisible()
  await interrupt.click()
  await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
  await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(divider)
}

/** Resume the queue that the interrupt paused, send one more turn, and prove that the agent answers it. */
async function expectAgentStillAnswers(page: Page, modelScript: ModelScript): Promise<void> {
  const pause = page.locator('[data-testid="queue-pause-button"]:visible')
  await expect(pause).toHaveText('Resume Queue')
  await pause.click()
  await expect(pause).toHaveText('Pause Queue')
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)
  await expectAssistantAnswer(page)
}

ohMyPiTest.describe('Oh My Pi interrupt', () => {
  ohMyPiTest('stops a model call and takes the next prompt', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // A held answer keeps the model call open until the interrupt ends it. The
    // mock counts the step when the request arrives, so the script stays
    // complete although the answer never reaches omp.
    await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
    await sendMessage(page, modelScript.prompt('Write a long essay about the history of computing.'))
    await modelScript.waitForSteps(1)

    await interruptTurn(page, /^Turn interrupted \(.+\)$/)
    await expectAgentStillAnswers(page, modelScript)
  })

  ohMyPiTest('stops a running command and takes the next prompt', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // The command outlives the test by far, so only the interrupt ends it. omp
    // asks the model nothing after an abort, so no step follows the call.
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'sleep-call', 'sleep 600')] })
    await sendMessage(page, modelScript.prompt('Wait for ten minutes.'))
    await modelScript.waitForSteps()
    // The command row exists before the interrupt, so the abort stops a command
    // that runs rather than a model call.
    await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: 'sleep 600' }).first()).toBeVisible()

    await interruptTurn(page, /^Turn interrupted \(.+\)1 tool$/)
    await expectAgentStillAnswers(page, modelScript)
  })
})
