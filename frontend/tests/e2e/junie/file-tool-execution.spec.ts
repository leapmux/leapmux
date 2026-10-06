import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseFileToolExecution, expectFileDiff } from '../helpers/nativeToolExecution'
import { bashToolCall, editToolCall, readToolCall } from '../helpers/providerToolCalls'
import { openWorkspace, sendMessage, toolRows, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, JUNIE_AGENT, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest.describe('Junie tool execution', () => {
  junieTest('runs a command, a read and an edit with its diff', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT)
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'junie-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    // Junie answers through its answer tool, which the provider's text step builds.
    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(context.provider, 'junie-shell', 'echo "junie-$((40 + 2))"')] },
      { toolCalls: [readToolCall(context.provider, 'junie-read', note)] },
      { toolCalls: [editToolCall(context.provider, 'junie-edit', { path: note, before: 'junie-before', after: 'junie-after' })] },
      nativeTextStep(context, 'All three tools ran.'),
    )
    await sendMessage(page, modelScript.prompt('Run the three scripted tools, then answer.'))
    await modelScript.waitForSteps(start + 4)
    await waitForAgentIdle(page)

    const tools = toolRows(page)
    await expect(tools.filter({ hasText: 'junie-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    await expectFileDiff(page, { before: 'junie-before', after: 'junie-after' })
  })
})

junieTest('reads and changes native files and keeps the applied diff after reload', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
