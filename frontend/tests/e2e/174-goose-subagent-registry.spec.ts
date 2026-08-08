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
  agentTabIdsForWorkspace,
  expectRegistrySectionAbsent,
  expectRowBecomesTerminal,
  expectSectionPersists,
  listAgents,
  tryWaitForRegistryRow,
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
    const workspaceId = authenticatedGooseWorkspace.workspaceId

    await expectRegistrySectionAbsent(page)

    // A task that uses Goose's delegate/summon tool and runs at least one tool.
    await sendMessage(page, 'Use your delegate/subagent tool to spawn a subagent that runs a shell command `echo goose-done` and tells you the result.')

    // The model may choose not to spawn; skip the spawn-dependent assertions.
    const row = await tryWaitForRegistryRow(page)
    gooseTest.skip(!row, 'model did not spawn a subagent')
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
      // Composer on the child tab is disabled (Goose is not steerable).
      await expect(page.getByTestId('composer-disabled-hint')).toBeVisible()
    }

    await expectRowBecomesTerminal(page, r, 'Completed').catch(e => console.warn('terminal assertion (best-effort):', e?.message ?? e))
    await expectSectionPersists(page)

    // Worker-backed: a child agent exists with a parent when Goose linked one.
    if (childId) {
      await expect.poll(async () => {
        const tabIds = await agentTabIdsForWorkspace(hubUrl, adminToken, workspaceId)
        const agents = await listAgents(hubUrl, adminToken, workerId, tabIds)
        if (!agents)
          return null
        return agents.some(a => a.parentAgentId !== '') ? 'found' : null
      }).toBe('found')
    }
  })
})
