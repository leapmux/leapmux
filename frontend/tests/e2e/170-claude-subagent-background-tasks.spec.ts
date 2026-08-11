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
  expectRowBecomesFinal,
  expectSectionPersists,
  listAgents,
  openChildTabFromRow,
  requireRegistryRow,
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

    // 2. Spawn a subagent.
    //
    // The subagent's job is to WRITE something, with no shell in it anywhere.
    // Asking it to `echo` a marker gave the model a one-line shortcut it took
    // every time -- it ran the echo itself as a background Bash, produced a
    // SHELL row instead of a subagent one, and the spec skipped on every run
    // while covering nothing. A task Bash cannot do removes the shortcut. The
    // prompt is directive about the TOOL as well, since the outcome alone did
    // not imply it.
    const MARKER = 'SUBAGENT-MARKER-1'
    await sendMessage(page, `You MUST use the Task tool. Spawn exactly one general-purpose subagent and give it this prompt verbatim: "Write two sentences about the ocean, then end your reply with the token ${MARKER}." Do not answer it yourself and do not use Bash. Wait for the subagent to finish, then tell me what it wrote.`)
    await waitForAgentIdle(page, 180_000)

    // 3. Sidebar: section + a subagent row. The model may choose not to spawn
    //    one at all, which is its discretion rather than a defect here, so the
    //    spawn-dependent assertions skip rather than fail.
    const row = await requireRegistryRow(test, page)
    await expect(backgroundTasksSection(page)).toBeVisible()

    // A clickable row is a <button>, which Oat's base button rule renders at
    // var(--font-medium). The row must override that and stay at the normal
    // weight, so a subagent does not read as emphasized against the shell rows.
    // Only a real browser resolves the cascade, so no unit test can see this.
    await expect(row).toHaveCSS('font-weight', '400')

    // 4. Wait for the row to link to a child transcript (EnsureChildAgent runs
    //    at task_started; the child-agent-id propagates via the next broadcast).
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')

    // 5. Open the tab from the sidebar row. The helper asserts the agent-tab
    //    count grew by exactly one and returns the new tab's id.
    const childTabId = await openChildTabFromRow(page, row)

    // 6. Worker-backed: the child agent exists with parent linkage + a
    //    non-empty spawn span id. Query the worker directly for that tab id.
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

    // 8. Completion: the row reaches a final status; the section persists.
    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)

    // 9. The registry's kind tabs. A subagent row lives under Subagents and not
    //    under Shell, and All shows it again. Only a real spawn puts a row in
    //    the section at all, which is why this rides on this test rather than
    //    standing alone.
    const kindTab = (key: string) => page.locator(`[data-testid="bg-task-filter-${key}"]:visible`)
    await expect(kindTab('all')).toHaveAttribute('aria-selected', 'true')
    await kindTab('subagent').click()
    await expect(row).toBeVisible()
    await kindTab('shell').click()
    await expect(row).not.toBeVisible()
    await kindTab('all').click()
    await expect(row).toBeVisible()

    // 10. Closing the parent tab takes its subagent tab with it. The child is a
    //     transcript the parent's process feeds, so left behind it is a tab
    //     nothing can add to. The worktree prompt is the parent's own and may or
    //     may not appear here (it depends on the working dir's git state), so
    //     answer it when it does.
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    const parentTab = page.locator(
      `[data-testid="tab"][data-tab-type="agent"]:not([data-tab-id="${childTabId}"])`,
    ).first()
    await parentTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

    // Wait for whichever the inspect produces -- the prompt, or the close going
    // straight through -- rather than reading visibility before either lands.
    const closeDialog = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Close Last Tab' }) })
    await expect.poll(async () =>
      await closeDialog.count() > 0 || await agentTabs.count() === 0,
    ).toBe(true)
    if (await closeDialog.count() > 0) {
      await closeDialog.getByRole('button', { name: 'Close anyway' }).click()
      await closeDialog.getByRole('button', { name: 'Confirm?' }).click()
    }

    await expect(page.locator(`[data-testid="tab"][data-tab-id="${childTabId}"]`)).toHaveCount(0)
    await expect(agentTabs).toHaveCount(0)
  })

  test('background shell appears as a non-clickable shell row', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace
    await sendMessage(page, 'Run `sleep 3 && echo BG-MARKER` with Bash run_in_background=true, then read the output with TaskOutput.')

    // Wait for the ROW, not for the agent to go idle. A running background task
    // keeps the thinking indicator up on purpose -- an active registry row IS
    // the agent still working -- so waiting for idle here races the very thing
    // the test is about, and times out whenever the shell outlives the turn.
    // The row is the observable this test wants anyway.
    //
    // Tolerate the model not honoring run_in_background: without a background
    // shell there is no row to assert on, which is its discretion, not a defect.
    const shellRow = await requireRegistryRow(test, page, 'shell')

    await expect(backgroundTasksSection(page)).toBeVisible()
    await expect(shellRow!).toHaveAttribute('data-child-agent-id', '')
    // A static row is a <div>. It must render at the same weight as the
    // clickable <button> row that this spec's first test pins.
    await expect(shellRow!).toHaveCSS('font-weight', '400')
  })
})
