import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracTest, expect, openDiracAgent } from '../dirac-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall, diracEditAnchorCapture, diracRespondToolCall, editToolCall, readToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

diracTest.describe('Dirac tool execution', () => {
  const PROVIDER = AgentProvider.DIRAC

  diracTest('runs a command, a read and an edit with its diff', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'dirac-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'dirac-shell', 'echo "dirac-$((40 + 2))"')] },
      { toolCalls: [readToolCall(PROVIDER, 'dirac-read', note)] },
      {
        toolCalls: [editToolCall(PROVIDER, 'dirac-edit', { path: note, before: 'dirac-before', after: 'dirac-after' })],
        captures: diracEditAnchorCapture('dirac-before'),
      },
      { toolCalls: [diracRespondToolCall('dirac-respond', 'complete', 'Both tools ran.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted tools, then complete.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)

    const tools = page.locator('[data-tool-message]:visible')
    await expect(tools.filter({ hasText: 'dirac-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: 'dirac-after' })
    await expect(diff.first()).toBeVisible()
    await expect(diff.first()).toContainText('dirac-before')
  })
})

diracTest('uses a prior native read anchor to edit the file and keeps its diff after reload', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseFileToolExecution(context, {
    editStep: (callId, path, before, after) => ({
      toolCalls: [editToolCall(context.provider, callId, { path, before, after })],
      captures: diracEditAnchorCapture(before),
    }),
  })
})
