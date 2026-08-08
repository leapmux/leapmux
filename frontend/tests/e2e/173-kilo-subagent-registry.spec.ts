import {
  agentTabIdsForWorkspace,
  backgroundTasksSection,
  expectNoChildAgents,
  expectRegistrySectionAbsent,
  expectRowBecomesTerminal,
  expectRowNotClickable,
  expectSectionPersists,
} from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
/**
 * 173 — KiloCode subagent registry (registry-only, same ACP layer as OpenCode).
 *
 * IMPORTANT: Kilo's default model is an image model that no-ops agentic turns;
 * the kilo fixture opens the agent with an explicit text-capable model so the
 * subagent spawn actually runs.
 */
import { expect, KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest.describe('Kilo subagent registry', () => {
  kiloTest('subagent spawn creates a registry row with no child transcript', async ({
    authenticatedKiloWorkspace,
    page,
    leapmuxServer,
  }) => {
    void authenticatedKiloWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = authenticatedKiloWorkspace.workspaceId

    await expectRegistrySectionAbsent(page)

    await sendMessage(page, 'Use your task tool to spawn a subagent that runs `echo kilo-done` and reports the result.')
    await waitForAgentIdle(page, 180_000)

    await expect(backgroundTasksSection(page)).toBeVisible()
    const row = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').first()
    await expect(row).toBeVisible()

    await expectRowBecomesTerminal(page, row, 'Completed')
    await expectSectionPersists(page)
    await expectRowNotClickable(page, row)
    const tabIds = await agentTabIdsForWorkspace(hubUrl, adminToken, workspaceId)
    await expectNoChildAgents(hubUrl, adminToken, workerId, tabIds)
  })
})
