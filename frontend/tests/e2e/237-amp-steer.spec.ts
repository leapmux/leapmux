import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest, expect } from './amp-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

/**
 * 237 — Amp steering.
 *
 * A message sent during a turn waits in LeapMux's input queue, and its Steer button
 * sends it into the running turn as a steering line. Amp inserts it at the turn's next
 * interruption point, after the running tool, and the model's next inference reads it.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('steers a running turn after its tool', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
  void authenticatedAmpWorkspace
  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.AMP, 'sleep-call', 'sleep 5')] },
    { text: 'finished steered' },
  )
  await sendMessage(page, modelScript.prompt('Run sleep 5 with your shell tool, then reply with one word: finished.'))
  await modelScript.waitForSteps(1)
  await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: 'sleep 5' }).first()).toBeVisible()
  await expect(page.getByTestId('interrupt-button')).toBeVisible()

  // The turn runs, so the message waits in the queue, which offers to steer with it.
  await sendMessage(page, 'Also append the word steered to your final reply.')
  const queued = page.getByTestId(/^queued-input-/).filter({ hasText: 'Also append the word steered' })
  await expect(queued).toBeVisible()
  await queued.getByRole('button', { name: 'Steer' }).click()
  await expect(queued).toHaveCount(0)

  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)
  await expect(assistantBubbles(page).filter({ hasText: 'finished steered' }).last()).toBeVisible()
  await expect(userBubbles(page).filter({ hasText: 'Also append the word steered' }).first()).toBeVisible()
  // The steering line reached the model at the interruption point after the tool,
  // inside the same turn: the one turn spent both steps.
  const second = status.requests.find(request => request.stepIndex === 1)
  expect(JSON.stringify(second?.body)).toContain('Also append the word steered to your final reply.')
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
})
