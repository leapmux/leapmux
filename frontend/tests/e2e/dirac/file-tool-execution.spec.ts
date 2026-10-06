import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { DIRAC_AGENT, diracTest, expect } from '../dirac-fixtures'
import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseFileToolExecution, expectFileDiff } from '../helpers/nativeToolExecution'
import { bashToolCall, diracEditAnchorCapture, editToolCall, readToolCall } from '../helpers/providerToolCalls'
import { expectNoControlBanner, openWorkspace, sendMessage, toolRows, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext } from './scenarios'

diracTest.describe('Dirac tool execution', () => {
  diracTest('runs a command, a read and an edit with its diff', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT)
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'dirac-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    // Dirac completes a turn through its respond tool, which the provider's text step builds.
    // The tools run with no permission request, so the turn waits for its steps with no click.
    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(context.provider, 'dirac-shell', 'echo "dirac-$((40 + 2))"')] },
      { toolCalls: [readToolCall(context.provider, 'dirac-read', note)] },
      {
        toolCalls: [editToolCall(context.provider, 'dirac-edit', { path: note, before: 'dirac-before', after: 'dirac-after' })],
        captures: diracEditAnchorCapture('dirac-before'),
      },
      nativeTextStep(context, 'Both tools ran.'),
    )
    await sendMessage(page, modelScript.prompt('Run the scripted tools, then complete.'))
    await modelScript.waitForSteps(start + 4)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)

    const tools = toolRows(page)
    await expect(tools.filter({ hasText: 'dirac-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    await expectFileDiff(page, { before: 'dirac-before', after: 'dirac-after' })
  })
})

diracTest('uses a prior native read anchor to edit the file and keeps its diff after reload', async ({ native }) => {
  await exerciseFileToolExecution(native, {
    editStep: (callId, path, before, after) => ({
      toolCalls: [editToolCall(native.provider, callId, { path, before, after })],
      captures: diracEditAnchorCapture(before),
    }),
  })
})
