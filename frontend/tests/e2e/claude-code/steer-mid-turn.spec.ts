import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest, claudeProcessTest as test } from '../claude-fixtures'
import { expectNativePdfPart, nativeUserStrings, scriptedRequest } from '../helpers/attachmentModelProbe'
import { attachFile, sendWithAttachment, writeAttachmentFixture } from '../helpers/attachments'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { queuedInputRow, steerButton, steerQueuedInput } from '../helpers/steer'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

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

  test('refuses to steer a PDF into a Claude turn and sends it as the next turn', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace

    // Claude Code folds a steered message into the running turn as a queued
    // command, and that fold drops a document block. The worker therefore
    // refuses to steer a PDF and sends the input as the next turn, document
    // and all.
    const gate = 'claude-pdf-steer'
    await modelScript.queue(
      { gate, text: 'First answer.' },
      { text: 'The document arrived.' },
    )
    await sendMessage(page, modelScript.prompt('Write the first answer.'))
    await modelScript.waitForGate(gate)

    const sourcePath = writeAttachmentFixture('pdf', 'steered.pdf')
    try {
      await attachFile(page, sourcePath)
      await sendWithAttachment(page, 'Read the attached PDF next.')
      const queued = queuedInputRow(page, 'Read the attached PDF')
      await expect(queued).toBeVisible()
      await steerButton(queued).click()
      expect((await modelScript.status()).requests.some(request => request.stepIndex === 1)).toBe(false)
    }
    finally {
      await modelScript.releaseGate(gate)
    }

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const second = scriptedRequest(status, 'anthropic-messages', 1)
    expectNativePdfPart(second, readFileSync(sourcePath))
    expect(nativeUserStrings(second.body).join('\n')).toContain('Read the attached PDF next.')
  })
})

claudeTest('delivers steering to the actual native tool turn before its single turn end', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
  await waitForSettingsIdle(page)
  // Claude Code streams no output of a running shell command. Its `tool_progress`
  // frame carries the elapsed time alone, so the first output reaches the
  // transcript only with the tool result, after the steer. The helper still
  // proves that the next native request read the first output.
  await exerciseSteerAfterTool({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }, { expectDisplayedOutput: false })
})
