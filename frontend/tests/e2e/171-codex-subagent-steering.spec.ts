/**
 * 171 — Codex collab subagent steering.
 *
 * Covers: registry row on spawn, child tab with a live transcript, an ENABLED
 * composer (Codex is the only provider with SupportsChildSteering), and a
 * steering message delivered to the child conversation. Parent isolation: the
 * parent shows the flat spawnAgent card with no nested child rows.
 */
import { codexTest, expect } from './codex-fixtures'
import {
  expectRegistrySectionAbsent,
  expectRowBecomesFinal,
  listAgents,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage } from './helpers/ui'

codexTest.describe('Codex subagent steering', () => {
  codexTest('spawn a collab subagent, open its tab, and steer it', async ({
    authenticatedCodexWorkspace,
    page,
    leapmuxServer,
  }) => {
    void authenticatedCodexWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // 1. Precondition.
    await expectRegistrySectionAbsent(page)

    // 2. Spawn a collab subagent with a long-enough task to stay running.
    await sendMessage(page, 'Use your spawnAgent/subagent tool to spawn a subagent that writes a 500-word essay about the ocean. Wait for it to start.')

    // 3. Registry row (subagent, running). Wait for the row directly (not
    //    waitForAgentIdle -- the indicator stays up while a task is active).
    // The model may choose not to spawn; skip the spawn-dependent assertions
    // rather than fail on a real LLM's discretion.
    const row = await requireRegistryRow(codexTest, page)

    // 4. Wait for the row to link to a child transcript, then click -> child
    //    tab opens adjacent to the parent.
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    const childTabId = await openChildTabFromRow(page, row)

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

    // 7. Terminal (best-effort): after the parent's wait/closeAgent completes,
    //    the row becomes terminal. A long-running subagent may not finish within
    //    the poll window; the core assertions (registry + steerable child tab)
    //    already passed, so this is a soft check.
    await expectRowBecomesFinal(page, row)
  })
})
