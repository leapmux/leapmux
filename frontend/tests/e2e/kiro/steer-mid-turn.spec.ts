import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { finishCleanup } from '../helpers/cleanup'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteeredReply, steerQueuedInput } from '../helpers/steer'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiroTest, openKiroAgent } from '../kiro-fixtures'

kiroTest.describe('Kiro interrupt, steering and process lifetime', () => {
  kiroTest('steers a running turn with a queued message', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const control = createToolOutputControl(workingDir)
    try {
      await modelScript.queue(
        { toolCalls: [bashToolCall(AgentProvider.KIRO, 'kiro-slow', control.command)] },
        { text: 'STEERED' },
      )
      await sendMessage(page, modelScript.prompt('Run the slow command, then report.'))
      await modelScript.waitForSteps(1)
      await control.waitForFirstOutput()
      await expect(page.locator('[data-testid="interrupt-button"]:visible')).toBeVisible()
      await steerQueuedInput(page, {
        message: modelScript.prompt('Reply with the single word STEERED when the command ends.'),
        match: 'Reply with the single word STEERED',
      })
      await control.releaseFirstOutput()
      await control.waitForSecondOutput()
    }
    finally {
      await finishCleanup([control.releaseFirstOutput(), control.releaseFinalOutput()])
    }
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const afterCommand = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(afterCommand?.body)).toContain('Reply with the single word STEERED')
    expect(JSON.stringify(afterCommand?.body)).toContain(control.firstMarker)
    expect(JSON.stringify(afterCommand?.body)).toContain(control.secondMarker)
    await expectSteeredReply(page, 'STEERED', 'first')
  })
})
