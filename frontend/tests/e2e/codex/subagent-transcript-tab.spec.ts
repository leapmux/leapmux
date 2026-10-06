/**
 * Codex subagent lifecycle and transcript routing.
 *
 * Covers: the V2 activity-based registry row, its readable title, a child tab
 * with an isolated read-only transcript and exact completion.
 */
import { expect } from '@playwright/test'
import { extractItem } from '../../../src/components/chat/providers/codex/extractors/item'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { codexTest } from '../codex-fixtures'
import { getTestChannel } from '../helpers/api'
import { stepRequest } from '../helpers/mockModelScript'
import { nativeAgentById, selectedAgentTabId } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexWaitAgentToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { expectNoRegistryRows, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, openWorkspace, sendMessage, tabById } from '../helpers/ui'
import { expectReadOnlySubagentReason } from '../helpers/unsupportedSubagent'
import { openProviderAgent } from '../helpers/workspace'
import { CODEX_AGENT } from './scenarios'

codexTest.describe('codex subagent lifecycle', () => {
  codexTest('opens and isolates a V2 subagent transcript', async ({ native }) => {
    const { page, leapmuxServer, modelScript } = native

    // 1. Precondition.
    await expectNoRegistryRows(page, leapmuxServer)
    const parentTabId = await selectedAgentTabId(page)

    // 2. Spawn one V2 subagent with a fixed canonical task name. Spell the
    // output marker as parts so it is absent from the root's user bubble.
    // `spawn_agent` takes the description with its spaces turned into
    // underscores, which is where the canonical task name comes from.
    const taskName = 'codex_probe_child'
    // A RULE rather than a queued step: the child runs its own turns, and how
    // many is the provider's business, not this test's.
    //
    // Matched on the BODY, not on the user text. `spawn_agent` FORKS the
    // parent's conversation -- `fork_turns` defaults to `all` -- so the child's
    // last user turn is the ROOT's prompt, and its own task arrives as an
    // `agent_message` addressed to it with the payload in `encrypted_content`.
    // A `user` matcher therefore sees the root's words in both agents and can
    // tell them apart in neither.
    //
    // Both patterns must hold: `NEW_TASK` appears only in an agent that RECEIVED
    // a task, and the task name pins it to this child rather than another.
    await modelScript.rule({
      name: 'the child answers with its marker',
      when: { body: ['NEW_TASK', taskName] },
      respond: { text: 'CHILD_DONE' },
    })
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(native.provider, 'spawn-child', {
          description: taskName.replaceAll('_', ' '),
          prompt: modelScript.prompt('reply with the child marker'),
        })],
      },
      { text: 'ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one child and report when it finishes.'))
    await modelScript.waitForSteps(start + 2)

    // 3. This request is explicit, so a missing row is a failure. The canonical
    // task path supplies the row and tab title before child output starts.
    const row = await requireRegistryRow(page)
    await expect(row).toContainText(taskName)

    // 4. Click the row. `openChildTabFromRow` waits until the row links a child
    //    transcript, and the child tab opens adjacent to the parent.
    const childTabId = await openChildTabFromRow(page, row)
    await expect(tabById(page, childTabId)).toContainText(taskName)

    // The child answer belongs only to the child transcript.
    const childAnswer = assistantBubbles(page).filter({ hasText: /^CHILD_DONE$/ })
    await expect(childAnswer).toBeVisible()

    // 5. Multi-Agent V2 rejects direct app-server input for spawned children.
    await expectReadOnlySubagentReason(page)

    // 6. Worker-backed: the child exists with parent linkage and reports the
    //    read-only capability. Query the worker directly for the child tab ID
    //    (the child tab propagates to the hub's ListTabs async).
    await retryUntilPass(async () => {
      const child = await nativeAgentById(native, childTabId)
      expect(child && !child.acceptsMessages ? 'read-only' : null, 'the Worker holds the child as a read-only agent').toBe('read-only')
    })

    // 7. Select the parent and prove the child answer did not leak into it.
    await tabById(page, parentTabId).click()
    await expect(assistantBubbles(page).filter({ hasText: /^CHILD_DONE$/ })).toHaveCount(0)

    // 8. The completed child turn is an exact final signal. A generic final
    // status would let a failed child pass this happy-path regression.
    await expect(row).toHaveAttribute('data-status', 'completed')
  })
})

codexTest.describe('provider tool rendering', () => {
  codexTest('reveals messages after an empty Codex wait result', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, CODEX_AGENT, { directoryPrefix: 'renderer-empty-codex-wait-' })
    // The native wait emits a completed agent item with no receivers or states.
    // Its model result reports the timeout. The empty agent item must reveal later rows.
    const callId = 'empty-wait'
    const stepIndex = await modelScript.queue(
      { toolCalls: [codexWaitAgentToolCall(callId, 1)] },
      { text: 'VISIBLE_AFTER_EMPTY_WAIT' },
    )
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await sendMessage(page, modelScript.prompt('Wait for the agents that are not running.'))
    const status = await modelScript.waitForSteps(stepIndex + 2)
    const nativeResult = nativeToolResult(stepRequest(status, stepIndex + 1), callId)
    expect(JSON.parse(nativeResult)).toMatchObject({ timed_out: true, message: expect.stringContaining('Wait timed out.') })
    expect(nativeResult).not.toMatch(/unsupported call|failed to parse function arguments/)

    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    await expect(chat.getByText('VISIBLE_AFTER_EMPTY_WAIT', { exact: true }).filter({ visible: true })).toBeVisible()

    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    if (!agentId)
      throw new Error('The empty native wait proof needs the Worker agent ID.')
    const workerId = leapmuxServer.workerId
    if (!workerId)
      throw new Error('The empty native wait proof needs an online Worker ID.')
    const response = await channel.callWorker(workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId, limit: 200 })
    const completed = response.messages
      .filter(message => message.spanId === callId && message.spanType === 'collabAgentToolCall')
      .map((message) => {
        const content = decompressContentToString(message.content, message.contentCompression)
        if (content === null)
          throw new Error('The native wait item could not be decompressed.')
        return extractItem(JSON.parse(content))
      })
      .filter(item => item?.id === callId && item.status === 'completed')
    expect(completed).toHaveLength(1)
    expect(completed[0]).toMatchObject({ type: 'collabAgentToolCall', tool: 'wait', prompt: null })
    expect(completed[0]?.receiverThreadIds).toEqual([])
    expect(completed[0]?.agentsStates).toEqual({})
    expect(completed[0]?.senderThreadId).toEqual(expect.any(String))
    expect(completed[0]?.senderThreadId).not.toBe('')
  })
})
