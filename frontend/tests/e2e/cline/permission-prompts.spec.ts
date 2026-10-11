import { existsSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CLINE_SPAWN_WARNING } from '../../../src/components/chat/providers/cline/spawnWarning'
import { CLINE_DECLINE_REASON } from '../../../src/generated/contracts/cline-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, expectDeclinedToolRowAcrossReload, expectSavedRefusalFeedback, toolResultCallId } from '../helpers/nativePermission'
import { bashToolCall, readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { chatText, expectNoControlBanner, expectSettingsChip, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * The test answers real native permission requests. Allow executes the tool. Deny must reach the next native model request as a refusal.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.describe('Cline control requests', () => {
  clineTest('runs a command after the reader allows it', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await expectSettingsChip(page, 'Act')
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'allow-call', 'echo "cline-$((40 + 2))"'),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('echo "cline-$((40 + 2))"')
        await expect(banner).toContainText('run_commands')
      },
      // The follow-up request carries the output of the command that ran.
      nativeProof: request => expect(JSON.stringify(request.body)).toContain('cline-42'),
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect.poll(() => chatText(page)).toContain('cline-42')
        await expect(savedControlAnswer(page)).toHaveText('Allow')
      },
    })
  })

  // Cline hands the reason to the model as the call's error, and the refused call reads declined.
  clineTest('refuses a command with the reader\'s reason, which reaches the model', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    const marker = join(askingClineWorkspace.workingDir, 'refused.txt')
    await exerciseNativePermissionReason(context, {
      toolCall: bashToolCall(context.provider, 'deny-call', `printf refused > ${marker}`),
      route: 'native-reply',
      beforeDecision: banner => expect(banner).toContainText(`printf refused > ${marker}`),
      expectNotRun: () => expect(existsSync(marker)).toBe(false),
      viewProof: async (reason) => {
        await expectNoControlBanner(page)
        await expectSavedRefusalFeedback(page, reason)
        await expectDeclinedToolRowAcrossReload(context, await toolResultCallId(page, reason), reason)
      },
    })
  })

  clineTest('reads a file without a banner, as Cline\'s own CLI does in Act', async ({ askingClineWorkspace, page, modelScript }) => {
    const notes = join(askingClineWorkspace.workingDir, 'notes.txt')
    writeFileSync(notes, 'cline-safe-read\n')
    // The turn waits for its steps with no click, so a banner would hold the turn and fail the step wait.
    const start = await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.CLINE, 'safe-read', notes)] },
      { text: 'I read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Read the notes.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    await expectNoControlBanner(page)
    await expect.poll(() => chatText(page)).toContain('cline-safe-read')
  })

  clineTest('warns that an approved subagent asks nothing, and a refusal reaches the model', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: spawnSubagentToolCall(context.provider, 'spawn-refused', {
        description: 'Refused helper',
        prompt: 'Never runs, because the reader refuses the spawn.',
      }),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('spawn_agent')
        await expect(banner).toContainText(CLINE_SPAWN_WARNING)
      },
      // A refusal with no words of the reader's gives the model LeapMux's own reason.
      nativeProof: request => expect(JSON.stringify(request.body)).toContain(CLINE_DECLINE_REASON.Tool),
      viewProof: async () => {
        await expectNoControlBanner(page)
        // LeapMux's own reason is no reason of the reader's, so the saved row states the decision alone.
        await expect(savedControlAnswer(page)).toHaveText('Deny')
      },
    })
  })
})
