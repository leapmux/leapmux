import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, fastAgentTest, openFastAgentAgent } from '../fastagent-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent tool execution', () => {
  const PROVIDER = AgentProvider.FAST_AGENT

  fastAgentTest('runs a command, a read and an edit with its diff', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'fast-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    // The negotiated ACP filesystem replaces Fast Agent's local edit tools.
    // The model receives the host read and write tools without `edit_file`.
    // Writing an existing file returns its old and new contents as a diff.
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'fast-shell', 'echo "fast-$((40 + 2))"')] },
      { toolCalls: [readToolCall(PROVIDER, 'fast-read', note)] },
      { toolCalls: [writeToolCall(PROVIDER, 'fast-edit', { path: note, content: 'fast-after' })] },
      { text: 'All three tools ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the three scripted tools, then report.'))
    const banner = page.locator('[data-testid="control-banner"]:visible')
    for (const step of [1, 2, 3]) {
      await modelScript.waitForSteps(step)
      await expect(banner).toBeVisible()
      await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    }
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)

    const tools = page.locator('[data-tool-message]:visible')
    await expect(tools.filter({ hasText: 'fast-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: 'fast-after' })
    await expect(diff.first()).toBeVisible()
    await expect(diff.first()).toContainText('fast-before')
  })
})

fastAgentTest('reads and changes native files and keeps the applied diff after reload', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseFileToolExecution(context, { editCall: (callId, path, _before, after) => writeToolCall(context.provider, callId, { path, content: `${after}\n` }) })
})
