import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest, expect } from './cline-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { expectSteeredReply, steerQueuedInput } from './helpers/steer'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  sendMessage,
  userBubbles,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 245 — Cline interrupt and steering.
 *
 * An interrupt aborts the session's run on the hub. Cline stops the model call or
 * the command, and ends the run as aborted, which ends the turn as interrupted. The
 * hub and the session stay up, so the next prompt continues the same conversation.
 * LeapMux pauses the input queue after an interrupt, as it does for every provider.
 *
 * A message sent during a turn waits in LeapMux's input queue, and its Steer button
 * sends it into the running turn. Cline takes it at the run's next step, after the
 * running tool, and the model's next call reads it.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

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
 * agent answers it in the SAME session: its model call carries the interrupted prompt.
 */
async function expectSessionContinues(page: Page, modelScript: ModelScript, interruptedPrompt: string): Promise<void> {
  const pause = page.locator('[data-testid="queue-pause-button"]:visible')
  await expect(pause).toHaveText('Resume Queue')
  await pause.click()
  await expect(pause).toHaveText('Pause Queue')
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)
  await expectAssistantAnswer(page)
  expect(JSON.stringify(status.requests.at(-1)?.body)).toContain(interruptedPrompt)
}

clineTest.describe('Cline interrupt', () => {
  clineTest('stops a model call and continues the session at the next prompt', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    // A held answer keeps the model call open until the interrupt ends it. The mock
    // counts the step when the call starts, so the script stays complete although
    // the answer never reaches Cline.
    await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
    await sendMessage(page, modelScript.prompt('Write a long essay about the history of computing.'))
    await modelScript.waitForSteps(1)

    await interruptTurn(page)
    await expectSessionContinues(page, modelScript, 'history of computing')
  })

  clineTest('stops a running command and continues the session at the next prompt', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    // The command outlives the test by far, so only the interrupt ends it. Cline asks
    // the model nothing after an abort, so no step follows the call.
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.CLINE, 'sleep-call', 'sleep 600')] })
    await sendMessage(page, modelScript.prompt('Wait for ten minutes.'))
    await modelScript.waitForSteps()
    // The command row exists before the interrupt, so the stop ends a command that
    // runs rather than a model call.
    await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: 'sleep 600' }).first()).toBeVisible()

    await interruptTurn(page)
    await expectSessionContinues(page, modelScript, 'Wait for ten minutes.')
  })
})

clineTest.describe('Cline steering', () => {
  clineTest('steers a running turn after its tool', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    // The command runs until the test creates the gate, so the steer always reaches
    // Cline while the tool runs, however slow the machine is.
    const gate = join(createTestDirectory('cline-steer-gate-'), 'release')
    const command = `while [ ! -e '${gate}' ]; do sleep 0.1; done`
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CLINE, 'gate-call', command)] },
      { text: 'finished steered' },
    )
    await sendMessage(page, modelScript.prompt('Run the waiting command with your shell tool, then reply with one word: finished.'))
    await modelScript.waitForSteps(1)
    await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: gate }).first()).toBeVisible()
    await expect(page.getByTestId('interrupt-button')).toBeVisible()

    // The turn runs, so the message waits in the queue, which offers to steer with it.
    await steerQueuedInput(page, {
      message: 'Also append the word steered to your final reply.',
      match: 'Also append the word steered',
    })
    // Cline holds the steer. The command ends now, and the run's next step reads it.
    writeFileSync(gate, '')

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectSteeredReply(page, 'finished steered', 'last')
    await expect(userBubbles(page).filter({ hasText: 'Also append the word steered' }).first()).toBeVisible()
    // The steering message reached the model after the tool, inside the same turn:
    // the one turn spent both steps.
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(second?.body)).toContain('Also append the word steered to your final reply.')
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  })
})
