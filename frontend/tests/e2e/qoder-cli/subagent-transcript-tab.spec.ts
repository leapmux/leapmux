import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { attachQoderWorkerFrames } from '../helpers/qoderWorkerFrames'
import { expectNoRegistryRows, expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { listAgentsViaAPI } from '../helpers/worktree'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI subagent registry', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.QODER

  qoderTest('follows one subagent from its spawn to its report, with its own transcript', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, askingQoderWorkspace.workspaceId)
    expect(agents).toHaveLength(1)
    const rootAgentId = agents[0]!.id
    await expectNoRegistryRows(page, leapmuxServer)

    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { user: 'Reply with the single word PONG' },
      respond: { reasoning: 'The task asks for one word.', text: 'PONG' },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-qoder', {
          description: 'Ask for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await attachQoderWorkerFrames(testInfo, leapmuxServer, rootAgentId)

    const row = await requireRegistryRow(page)
    await expect(row).toContainText('Ask for one word')

    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'completed')
    expect((await modelScript.status()).ruleMatches['the child answers its one-word task']).toBe(1)
    await expect(assistantBubbles(page).filter({ hasText: 'The subagent reported PONG.' })).toBeVisible()

    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
    const childAgentId = await row.getAttribute('data-child-agent-id')
    expect(childAgentId).toBeTruthy()
    await attachQoderWorkerFrames(testInfo, leapmuxServer, childAgentId!, 'child')
    await openChildTabFromRow(page, row)
    await expect(assistantBubbles(page).filter({ hasText: 'PONG' }).first()).toBeVisible()
  })
})
