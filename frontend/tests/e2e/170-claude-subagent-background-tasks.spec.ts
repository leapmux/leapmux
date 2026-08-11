/**
 * 170 — Claude Code subagent + background tasks (the full flow).
 *
 * Covers: registry section appears on spawn, opening the subagent tab adjacent
 * to the parent, parent/child transcript isolation (worker-backed), and a
 * background shell row. Claude is the only provider with full-fidelity child
 * transcripts (--forward-subagent-text).
 *
 * Resilience notes: the row's child-agent-id lands via EnsureChildAgent, which
 * the section renders asynchronously; the spec polls for a non-empty id before
 * clicking. The shell test tolerates the model not choosing run_in_background
 * (it asserts only when a shell row actually appears).
 */
import { expect, test } from './fixtures'
import {
  backgroundTasksSection,
  expectRegistrySectionAbsent,
  expectRowBecomesTerminal,
  expectSectionPersists,
  listAgents,
} from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

test.describe('Claude subagent background tasks', () => {
  test('subagent spawn creates a registry row, a child tab, and isolates the transcript', async ({
    authenticatedWorkspace,
    page,
    leapmuxServer,
  }) => {
    void authenticatedWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // 1. Precondition: no registry section yet.
    await expectRegistrySectionAbsent(page)

    // 2. Spawn a subagent with a recognizable marker.
    const MARKER = 'SUBAGENT-MARKER-1'
    await sendMessage(page, `Use the Task tool to spawn a subagent (general-purpose) that runs \`echo ${MARKER}\` with Bash, then reports the output. Wait for the subagent to finish.`)
    await waitForAgentIdle(page, 180_000)

    // 3. Sidebar: section + a subagent row.
    await expect(backgroundTasksSection(page)).toBeVisible()
    const row = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').first()
    await expect(row).toBeVisible()

    // A clickable row is a <button>, which Oat's base button rule renders at
    // var(--font-medium). The row must override that and stay at the normal
    // weight, so a subagent does not read as emphasized against the shell rows.
    // Only a real browser resolves the cascade, so no unit test can see this.
    await expect(row).toHaveCSS('font-weight', '400')

    // 4. Wait for the row to link to a child transcript (EnsureChildAgent runs
    //    at task_started; the child-agent-id propagates via the next broadcast).
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')

    // 5. Open the tab from the sidebar row.
    const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
    await row.click()

    // 6. The agent-tab count increments by exactly 1.
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(tabsBefore + 1)

    // 7. Worker-backed: the child agent exists with parent linkage + a
    //    non-empty spawn span id. Read the child's tab id from the DOM (the
    //    newly-opened tab) and query the worker directly for it.
    const childTabId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').nth(tabsBefore).getAttribute('data-tab-id') ?? ''
    expect(childTabId).not.toBe('')
    let child: { id: string, parentAgentId: string, spawnSpanId: string } | null = null
    await expect.poll(async () => {
      const agents = await listAgents(hubUrl, adminToken, workerId, [childTabId])
      if (!agents)
        return null
      const found = agents.find(a => a.id === childTabId)
      child = found ? { id: found.id, parentAgentId: found.parentAgentId, spawnSpanId: found.spawnSpanId } : null
      return child
    }).not.toBeNull()
    expect(child!.parentAgentId).not.toBe('')
    expect(child!.spawnSpanId).not.toBe('')

    // 8. Completion: the row becomes terminal; the section persists.
    await expectRowBecomesTerminal(page, row, 'Completed')
    await expectSectionPersists(page)
  })

  test('background shell appears as a non-clickable shell row', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace
    await sendMessage(page, 'Run `sleep 3 && echo BG-MARKER` with Bash run_in_background=true, then read the output with TaskOutput.')
    await waitForAgentIdle(page, 180_000)

    await expect(backgroundTasksSection(page)).toBeVisible()
    // If the model honored run_in_background, a shell row exists and is not
    // clickable (no child-agent-id). Tolerate the model not honoring it: assert
    // only when a shell row is present.
    const shellRow = page.locator('[data-testid="bg-task-row"]:visible[data-kind="shell"]').first()
    const present = await shellRow.isVisible().catch(() => false)
    if (present) {
      await expect(shellRow).toHaveAttribute('data-child-agent-id', '')
      // A static row is a <div>. It must render at the same weight as the
      // clickable <button> row that spec 170's first test pins.
      await expect(shellRow).toHaveCSS('font-weight', '400')
    }
  })
})
