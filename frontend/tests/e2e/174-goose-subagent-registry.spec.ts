/**
 * 174 — Goose subagent registry + tool-request transcript.
 *
 * Goose surfaces tool REQUESTS (never results) over ACP via
 * _meta.toolNotification. The row IS clickable (it owns a tool-request
 * transcript): clicking opens a child tab showing "Requested tool: <name>"
 * cards. Worker-backed: the child agent exists with parent linkage. The child
 * composer is disabled (Goose is not steerable).
 */
import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import {
  expectRegistrySectionAbsent,
  expectRowBecomesFinal,
  expectSectionPersists,
  listAgents,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage } from './helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest.describe('Goose subagent registry', () => {
  gooseTest('delegate spawn creates a clickable row with a tool-request transcript', async ({
    authenticatedGooseWorkspace,
    page,
    leapmuxServer,
  }) => {
    void authenticatedGooseWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    await expectRegistrySectionAbsent(page)

    // A task that uses Goose's delegate/summon tool and runs at least one tool.
    await sendMessage(page, 'Use your delegate/subagent tool to spawn a subagent that runs a shell command `echo goose-done` and tells you the result.')

    // The model may choose not to spawn; skip the spawn-dependent assertions.
    const row = await requireRegistryRow(gooseTest, page)
    const r = row!

    // The row links to a tool-request transcript (child-agent-id) when Goose
    // surfaces a subagent_tool_request. Best-effort: the link can lag or be
    // absent if the subagent made no tool requests. Poll for the attribute
    // (NOT Promise.race against a timer -- getAttribute resolves immediately
    // with null and wins the race at t≈0).
    let childId: string | null = null
    try {
      await expect.poll(async () => {
        const v = await r.getAttribute('data-child-agent-id')
        return v ?? ''
      }).not.toBe('')
      childId = await r.getAttribute('data-child-agent-id')
    }
    catch {
      childId = null
    }

    if (childId) {
      // Click -> child tab opens adjacent to the parent.
      const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
      await r.click()
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(tabsBefore + 1)
      // Composer on the child tab is disabled (Goose is not steerable), and
      // the box itself says WHY. The placeholder used to blame a lost
      // connection the read-only transcript never had; asserting it here is
      // what proves the reason reaches the editor, since the plugin's unit
      // test cannot see the prop chain that feeds it.
      const noMessages = 'This subagent doesn\'t accept messages.'
      await expect(page.locator(`[data-placeholder="${noMessages}"]:visible`)).toBeVisible()
      // ONCE, not twice. The reason used to render again as a note above the
      // box, so a read-only subagent tab said the same sentence twice, a few
      // pixels apart.
      await expect(page.getByText(noMessages, { exact: true })).toHaveCount(0)
    }

    await expectRowBecomesFinal(page, r)
    await expectSectionPersists(page)

    // Worker-backed: a child agent exists with a parent when Goose linked one.
    if (childId) {
      // Ask the worker about THIS child id (read off the registry row), the way
      // 170/171 do. Seeding from the hub's tab list instead made this
      // unreachable: tabs live in the user CRDT and the hub's tab projection is
      // empty here, so the id list was always [].
      await expect.poll(async () => {
        const agents = await listAgents(hubUrl, adminToken, workerId, [childId!])
        const child = agents?.find(a => a.id === childId)
        if (!child)
          return null
        return {
          hasParent: child.parentAgentId !== '',
          hasSpawnSpan: child.spawnSpanId !== '',
          acceptsMessages: child.acceptsMessages,
        }
      }).toEqual({
        hasParent: true,
        hasSpawnSpan: true,
        // Goose cannot steer a subagent, so the child tab is a read-only
        // transcript -- the same fact the disabled composer above shows.
        acceptsMessages: false,
      })
    }
  })
})
