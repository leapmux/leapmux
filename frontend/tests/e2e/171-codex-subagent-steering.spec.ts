/**
 * 171 — Codex collab subagent steering.
 *
 * Covers: the V2 activity-based registry row, its readable title, a child tab
 * with an isolated transcript, an enabled composer, and exact completion.
 */
import { codexTest, expect } from './codex-fixtures'
import {
  expectNoRegistryRows,
  listAgents,
  openChildTabFromRow,
  waitForRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage } from './helpers/ui'

codexTest.describe('codex subagent steering', () => {
  codexTest('opens and isolates a V2 subagent transcript', async ({
    authenticatedCodexWorkspace,
    page,
    leapmuxServer,
  }) => {
    void authenticatedCodexWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // 1. Precondition.
    await expectNoRegistryRows(page)

    // 2. Spawn one V2 subagent with a fixed canonical task name. Spell the
    // output marker as parts so it is absent from the root's user bubble.
    const taskName = 'codex_probe_child'
    await sendMessage(page, `You MUST use spawn_agent exactly once with task_name "${taskName}". Tell the child to reply with the string formed by joining CHILD, an underscore, and DONE. Use wait_agent until it finishes. Do not quote the child's answer. Then reply with exactly ROOT_DONE.`)

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
    const childAnswer = assistantBubbles(page).filter({ hasText: 'CHILD_DONE' })
    await expect(childAnswer).toBeVisible()

    // 5. Composer: enabled (Codex is steerable), so the box carries its normal
    //    placeholder rather than any disabled reason.
    await expect(page.locator('[data-placeholder="Send a message..."]:visible')).toBeVisible()

    // 6. Worker-backed: the child exists with parent linkage and accepts
    //    messages. Query the worker directly for the child tab id read above
    //    (the child tab propagates to the hub's ListTabs async).
    await expect.poll(async () => {
      const agents = await listAgents(hubUrl, adminToken, workerId, [childTabId])
      if (!agents)
        return null
      const child = agents.find(a => a.id === childTabId)
      return child && child.acceptsMessages ? 'steerable' : null
    }).toBe('steerable')

    // 7. Select the parent and prove the child answer did not leak into it.
    await page.locator(`[data-testid="tab"][data-tab-id="${parentTabId}"]`).click()
    await expect(assistantBubbles(page).filter({ hasText: 'CHILD_DONE' })).toHaveCount(0)

    // 8. The completed child turn is an exact final signal. A generic final
    // status would let a failed child pass this happy-path regression.
    await expect(row).toHaveAttribute('data-status', 'completed')
  })
})
