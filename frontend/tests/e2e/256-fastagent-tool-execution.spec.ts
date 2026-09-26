import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest, openFastAgentAgent } from './fastagent-fixtures'
import { bashToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.FAST_AGENT

fastAgentTest.describe('Fast Agent tool execution', () => {
  fastAgentTest('runs a command, a read and an edit with its diff', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const note = join(workingDir, 'note.txt')
    writeFileSync(note, 'fast-before\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    // fast-agent's edit primitive under a host filesystem is a whole-file
    // write: when the client negotiates fs read+write (LeapMux does, for
    // Dirac's edit_file), fast-agent replaces its local edit tools with the
    // host-backed read/write pair and offers the model no `edit_file`. The
    // write of an existing file reports its own old/new diff, which is the
    // edit-with-diff this spec reads.
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'fast-shell', 'echo "fast-$((40 + 2))"')] },
      { toolCalls: [readToolCall(PROVIDER, 'fast-read', note)] },
      { toolCalls: [writeToolCall(PROVIDER, 'fast-edit', { path: note, content: 'fast-after' })] },
      { text: 'All three tools ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the three scripted tools, then report.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const tools = page.locator('[data-tool-message]:visible')
    await expect(tools.filter({ hasText: 'fast-42' }).first()).toBeVisible()
    await expect(tools.filter({ hasText: 'note.txt' }).first()).toBeVisible()
    const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: 'fast-after' })
    await expect(diff.first()).toBeVisible()
    await expect(diff.first()).toContainText('fast-before')
  })
})
