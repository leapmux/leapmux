import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseSteerAfterTool, exerciseSteerDuringText } from '../helpers/nativeToolSteering'
import { queuedInputRow } from '../helpers/steer'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

// Amp's stream JSON states a shell command only in its call and in its result, so the
// running row shows no output. The steering line reaches the model after the tool result.
ampTest('steers a running turn after its tool', async ({ native }) => {
  await exerciseSteerAfterTool(native, { expectDisplayedOutput: false })
})

ampTest('places a steer after the complete answer that precedes it', async ({ native }) => {
  await sendNativeAnswer(native, 'Create the native thread for the steering scenario.', 'The native thread is ready.')
  // Amp emits completed assistant messages. It sends no native text-progress frames.
  await exerciseSteerDuringText(native, { answer: 'continue', progress: 'model-stream' })
})

ampTest('keeps the first queued input until the new thread identifies itself', async ({ native }) => {
  const { page, modelScript } = native
  const gate = 'amp-first-thread-answer'
  await withCleanup(async () => {
    const start = await modelScript.queue({ text: 'The new thread answered.', gate }, { text: 'The queued message answered.' })
    await sendMessage(page, modelScript.prompt('Create the new native thread.'))
    await modelScript.waitForGate(gate)
    await sendMessage(page, modelScript.prompt('Queue guidance before the thread identity.'))
    const queued = queuedInputRow(page, 'Queue guidance before the thread identity.')
    await expect(queued).toBeVisible()
    await expect(queued.getByRole('button', { name: 'Steer', exact: true })).toHaveCount(0)
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The queued message answered.' })).toHaveCount(1)
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})
