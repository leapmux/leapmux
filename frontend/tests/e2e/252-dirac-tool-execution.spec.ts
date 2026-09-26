import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { DIRAC_E2E_SKIP_REASON, diracTest, expect, openDiracAgent } from './dirac-fixtures'
import { bashToolCall, diracEditAnchorCapture, diracRespondToolCall, editToolCall, readToolCall } from './helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.DIRAC

diracTest.describe('Dirac tool execution', () => {
  // Dirac's edit_file names its target line by an ANCHOR§CONTENT coordinate
  // that a prior anchored read assigns (the id is conversation-scoped and
  // opaque), so the script reads first and the edit step captures the
  // coordinate out of the request.
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
    await waitForAgentIdle(page, 120_000)

    const tools = page.locator('[data-tool-message]:visible')
    await expect(tools.filter({ hasText: 'dirac-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: 'dirac-after' })
    await expect(diff.first()).toBeVisible()
    await expect(diff.first()).toContainText('dirac-before')
  })
})
