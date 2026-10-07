import { existsSync, readFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { QWEN_TOOL } from '../../../src/generated/contracts/qwen-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRememberedAllow, expectDeclinedToolRow, toolResultCallId } from '../helpers/nativePermission'
import { bashToolCall } from '../helpers/providerToolCalls'
import { answerControl, assistantBubbles, enterControlFeedback, expectNoControlBanner, expectSettingsChip, openWorkspace, PLATFORM_MOD, savedControlAnswer, sendMessage, toolCallRow, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

import { openProviderAgent } from '../helpers/workspace'
import { qwenTest } from '../qwen-fixtures'
import { nativeContext, QWEN_AGENT } from './scenarios'

const PROVIDER = AgentProvider.QWEN_CODE

qwenTest.describe('Qwen Code control requests', () => {
  // Qwen's `default` mode asks before a shell command. The reply contains one of the offered options.
  // The native reply has no reason field. The reader's reason follows in a separate message.
  qwenTest('approves one command and rejects the next with a reason', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Default')
    const approved = join(workingDir, 'approved.txt')
    const rejected = join(workingDir, 'rejected.txt')
    const reason = 'Do not create the second file.'

    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'qwen-approved', `touch ${approved}`)] },
      { toolCalls: [bashToolCall(PROVIDER, 'qwen-rejected', `touch ${rejected}`)] },
    )
    await sendMessage(page, modelScript.prompt('Create the two scripted files.'))
    await modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText(`touch ${approved}`)
    expect(existsSync(approved)).toBe(false)
    await answerControl(page, 'allow')

    await modelScript.waitForSteps(start + 2)
    await expect(banner).toContainText(`touch ${rejected}`)
    expect(existsSync(approved)).toBe(true)
    // The reason ends the rejected turn and opens the next one, which answers it.
    const next = await modelScript.queue({ text: 'I will not create the second file.' })
    await enterControlFeedback(page, reason)
    await page.keyboard.press(`${PLATFORM_MOD}+Enter`)
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps(next + 1)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    await expect(userBubbles(page).filter({ hasText: reason }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I will not create the second file.' })).toBeVisible()
    // The next turn carries the reason to the model.
    expect(JSON.stringify((await modelScript.requestAt(next)).body)).toContain(reason)
    expect(existsSync(rejected)).toBe(false)
    // The rejected command reads declined, and its result row states the refusal of Qwen.
    // Qwen opens a streamed call with no input. The permission request states the command,
    // and the Worker stores that input with the request row. So the request row states it.
    // The test finds the result row by its refusal, so it does not depend on how Qwen renders the call ID.
    const refusal = `Tool "${QWEN_TOOL.RunShellCommand}" was canceled by the user.`
    const callId = await toolResultCallId(page, refusal)
    await expectDeclinedToolRow(page, callId, refusal)
    await expect(toolCallRow(page, callId, 'request')).toContainText(`touch ${rejected}`)
    // Each saved row reads the name of Qwen's own option. The reason is the row of the next message.
    await expect(savedControlAnswer(page)).toHaveText(['Allow', 'Reject'])
  })

  // Qwen keeps a project answer as a rule for the command in the settings of the project, so the same command later
  // runs with no request.
  qwenTest('a project answer covers the same command in the next turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Default')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const file = join(workingDir, 'qwen-project.txt')
    // Each run appends the marker, so the file states how many runs happened.
    const command = `printf qwen-project >> ${file}`
    await exerciseRememberedAllow(context, {
      scope: 'Project',
      firstCall: bashToolCall(PROVIDER, 'qwen-project-first', command),
      secondCall: bashToolCall(PROVIDER, 'qwen-project-second', command),
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      firstProof: () => expect(readFileSync(file, 'utf8')).toBe('qwen-project'),
      secondProof: () => expect(readFileSync(file, 'utf8')).toBe('qwen-projectqwen-project'),
    })
  })
})
