import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { submitGoal } from '../helpers/goalsAndTodos'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { steerQueuedInput } from '../helpers/steer'
import { interruptButton, sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest.describe('Codex session goal', () => {
  codexTest('offers Steer for input queued during a goal turn', async ({
    authenticatedCodexWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedCodexWorkspace

    // Start the process before the side-band goal command asks Codex to start
    // its own turn. That turn has no queue input to supply its classification.
    const start = await modelScript.queue({ text: 'ready' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    // The objective carries the marker because CODEX starts the next turn
    // itself, with the objective as its prompt: an unmarked goal reaches the
    // ambient scenario, which refuses it. The goal turn must still be RUNNING
    // when the steer arrives, so its answer is held open.
    await modelScript.fallback({ text: 'Working on the objective.', delayMs: 120_000 })
    await submitGoal(page, modelScript.prompt('Inspect this repository until I send a steering message. Do not stop before that message.'))

    // The Interrupt button proves that the provider-started goal turn runs.
    // Send while that condition still holds, so the message enters the queue.
    await expect(interruptButton(page)).toBeVisible()
    await steerQueuedInput(page, {
      message: modelScript.prompt('Stop now, mark the goal complete, and reply with STEERED.'),
      match: 'Stop now',
    })
  })
})

codexTest('delivers steering to the actual native tool turn before its single turn end', async ({ native }) => {
  await exerciseSteerAfterTool(native, { expectDisplayedOutput: false })
})
