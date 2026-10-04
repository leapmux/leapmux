import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { steerQueuedInput } from '../helpers/steer'
import { expandGoalsAndTodosSection, goalAction } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest.describe('Codex session goal', () => {
  codexTest('offers Steer for input queued during a goal turn', async ({
    authenticatedCodexWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedCodexWorkspace

    // Start the process before the side-band goal command asks Codex to start
    // its own turn. That turn has no queue input to supply its classification.
    await modelScript.queue({ text: 'ready' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)
    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    // The objective carries the marker because CODEX starts the next turn
    // itself, with the objective as its prompt: an unmarked goal reaches the
    // ambient scenario, which refuses it. The goal turn must still be RUNNING
    // when the steer arrives, so its answer is held open.
    await modelScript.fallback({ text: 'Working on the objective.', delayMs: 120_000 })
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(
      modelScript.prompt('Inspect this repository until I send a steering message. Do not stop before that message.'),
    )
    await page.locator('[data-testid="set-goal-submit"]:visible').click()

    // The Interrupt button proves that the provider-started goal turn runs.
    // Send while that condition still holds, so the message enters the queue.
    await expect(page.getByTestId('interrupt-button')).toBeVisible()
    await steerQueuedInput(page, {
      message: modelScript.prompt('Stop now, mark the goal complete, and reply with STEERED.'),
      match: 'Stop now',
    })
  })
})

codexTest('delivers steering to the actual native tool turn before its single turn end', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseSteerAfterTool({ page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }, { expectDisplayedOutput: false })
})
