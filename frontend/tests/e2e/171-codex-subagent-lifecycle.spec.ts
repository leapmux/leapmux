/**
 * 171 — Codex subagent lifecycle and transcript routing.
 *
 * Covers: the V2 activity-based registry row, its readable title, a child tab
 * with an isolated read-only transcript and exact completion.
 */
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  listAgents,
  openChildTabFromRow,
  waitForRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage } from './helpers/ui'

codexTest.describe('codex subagent lifecycle', () => {
  codexTest('opens and isolates a V2 subagent transcript', async ({
    authenticatedCodexWorkspace,
    page,
    leapmuxServer,
    modelScript,
  }) => {
    void authenticatedCodexWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // 1. Precondition.
    await expectNoRegistryRows(page)

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
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.CODEX, 'spawn-child', {
        description: taskName.replaceAll('_', ' '),
        prompt: modelScript.prompt('reply with the child marker'),
      })],
    })
    await modelScript.queue({ text: 'ROOT_DONE' })
    await sendMessage(page, modelScript.prompt('Spawn one child and report when it finishes.'))
    await modelScript.waitForSteps(2)

    // 3. This request is explicit, so a missing row is a failure. The canonical
    // task path supplies the row and tab title before child output starts.
    const parentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
    const parentTabId = await parentTab.getAttribute('data-tab-id') ?? ''
    expect(parentTabId).not.toBe('')
    const row = await waitForRegistryRow(page)
    await expect(row).toContainText(taskName)

    // 4. Wait for the row to link to a child transcript, then click -> child
    //    tab opens adjacent to the parent.
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    const childTabId = await openChildTabFromRow(page, row)
    await expect(page.locator(`[data-testid="tab"][data-tab-id="${childTabId}"]`)).toContainText(taskName)

    // The child answer belongs only to the child transcript.
    const childAnswer = assistantBubbles(page).filter({ hasText: /^CHILD_DONE$/ })
    await expect(childAnswer).toBeVisible()

    // 5. Multi-Agent V2 rejects direct app-server input for spawned children.
    await expect(page.locator('[data-placeholder="This subagent doesn\'t accept messages."]:visible')).toBeVisible()

    // 6. Worker-backed: the child exists with parent linkage and reports the
    //    read-only capability. Query the worker directly for the child tab ID
    //    (the child tab propagates to the hub's ListTabs async).
    await expect.poll(async () => {
      const agents = await listAgents(hubUrl, adminToken, workerId, [childTabId])
      if (!agents)
        return null
      const child = agents.find(a => a.id === childTabId)
      return child && !child.acceptsMessages ? 'read-only' : null
    }).toBe('read-only')

    // 7. Select the parent and prove the child answer did not leak into it.
    await page.locator(`[data-testid="tab"][data-tab-id="${parentTabId}"]`).click()
    await expect(assistantBubbles(page).filter({ hasText: /^CHILD_DONE$/ })).toHaveCount(0)

    // 8. The completed child turn is an exact final signal. A generic final
    // status would let a failed child pass this happy-path regression.
    await expect(row).toHaveAttribute('data-status', 'completed')
  })
})
