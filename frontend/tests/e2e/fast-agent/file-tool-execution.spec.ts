import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseFileToolExecution, expectFileDiff } from '../helpers/nativeToolExecution'
import { bashToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { answerControl, expectNoControlBanner, openWorkspace, sendMessage, toolRows, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { FAST_AGENT_AGENT, nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent tool execution', () => {
  fastAgentTest('runs a command, a read and an edit with its diff', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'fast-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    // The negotiated ACP filesystem replaces Fast Agent's local edit tools.
    // The model receives the host read and write tools without `edit_file`.
    // Writing an existing file returns its old and new contents as a diff.
    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(context.provider, 'fast-shell', 'echo "fast-$((40 + 2))"')] },
      { toolCalls: [readToolCall(context.provider, 'fast-read', note)] },
      { toolCalls: [writeToolCall(context.provider, 'fast-edit', { path: note, content: 'fast-after' })] },
      nativeTextStep(context, 'All three tools ran.'),
    )
    await sendMessage(page, modelScript.prompt('Run the three scripted tools, then report.'))
    // Each of the three tools asks once, after the agent requested its step.
    for (const offset of [1, 2, 3]) {
      await modelScript.waitForSteps(start + offset)
      await waitForControlBanner(page)
      await answerControl(page, 'allow')
    }
    await modelScript.waitForSteps(start + 4)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)

    const tools = toolRows(page)
    await expect(tools.filter({ hasText: 'fast-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    await expectFileDiff(page, { before: 'fast-before', after: 'fast-after' })
  })
})

fastAgentTest('reads and changes native files and keeps the applied diff after reload', async ({ native }) => {
  await exerciseFileToolExecution(native, { editCall: (callId, path, _before, after) => writeToolCall(native.provider, callId, { path, content: `${after}\n` }) })
})
