import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { steerQueuedInput } from '../helpers/steer'
import { chooseSettingsOption, sendMessage, waitForSettingsIdle } from '../helpers/ui'
import { processTest as test } from '../process-control-fixtures'

test.describe('agent input queue', () => {
  test('offers Steer for input queued during a Claude turn', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace

    // Keep the first turn active long enough to put the next message in the
    // durable queue. The Interrupt button is the Worker's turn-state signal.
    // The HOLD is what keeps it active: against the mock endpoint a long prompt
    // finishes as fast as a short one, so the turn has to be held open.
    await modelScript.queue({ text: 'A report.', delayMs: 60_000 })
    modelScript.allowUnconsumed('the steer ends the turn before the held answer arrives')
    await sendMessage(page, modelScript.prompt('Write a 2,000-word technical report about Go concurrency.'))
    await modelScript.waitForSteps()
    await expect(page.getByTestId('interrupt-button')).toBeVisible()

    await steerQueuedInput(page, {
      message: modelScript.prompt('Stop the report now and reply with the single word STEERED.'),
      match: 'Stop the report',
    })
  })
})

claudeTest('delivers steering to the actual native tool turn before its single turn end', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
  await waitForSettingsIdle(page)
  await exerciseSteerAfterTool({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId })
})
