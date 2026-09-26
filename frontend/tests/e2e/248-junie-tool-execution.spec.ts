import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, editToolCall, junieAnswerToolCall, readToolCall } from './helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.JUNIE

junieTest.describe('Junie tool execution', () => {
  junieTest('runs a command, a read and an edit with its diff', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'junie-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.rule(
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Tool run' } },
    )
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'junie-shell', 'echo "junie-$((40 + 2))"')] },
      { toolCalls: [readToolCall(PROVIDER, 'junie-read', note)] },
      { toolCalls: [editToolCall(PROVIDER, 'junie-edit', { path: note, before: 'junie-before', after: 'junie-after' })] },
      { toolCalls: [junieAnswerToolCall('junie-answer', 'All three tools ran.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the three scripted tools, then answer.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const tools = page.locator('[data-tool-message]:visible')
    await expect(tools.filter({ hasText: 'junie-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: 'junie-after' })
    await expect(diff.first()).toBeVisible()
    await expect(diff.first()).toContainText('junie-before')
  })
})
