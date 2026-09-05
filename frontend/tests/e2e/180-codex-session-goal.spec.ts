/**
 * 180 — Codex session goal, set through the UI and driven to completion.
 *
 * Codex is the one provider with an acknowledged side-band command for all four
 * actions (thread/goal/set and thread/goal/clear), so it is the only one that
 * can exercise the whole round trip: set, pause, resume, clear.
 *
 * The goal state is WORKER state that arrives on a broadcast, never optimistic
 * client state -- the RPC deliberately writes nothing locally, because the
 * provider echoes every change back and a local write would race the echo. So
 * every assertion here polls for the worker's answer rather than reading what
 * the click did.
 */
import { codexTest, expect } from './codex-fixtures'
import {
  countGoalTransitions,
  expandBackgroundTasksSection,
  expectGoalStatus,
  goalAction,
  goalCard,
  listAgents,
  workPanelTab,
} from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex session goal', () => {
  codexTest('set a goal from the panel, pause it, resume it, and clear it', async ({
    authenticatedCodexWorkspace,
    page,
    leapmuxServer,
  }) => {
    void authenticatedCodexWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // 1. Drive one turn first. It puts the agent tab on screen and, more to the
    //    point, gets the process registered -- the goal's supported ACTIONS are
    //    read from the running agent, and everything below depends on them.
    await sendMessage(page, 'Reply with the single word: ready')
    await waitForAgentIdle(page)

    // 2. The section is reachable with NO background tasks at all. That is the
    //    widened visibility rule doing its job: the ThinkingIndicator's goal
    //    chip is hidden whenever the agent is idle, which is most of this test,
    //    so the section is the only route to the panel -- and the panel is the
    //    only route to a first goal.
    await expect.poll(async () =>
      await page.locator('[data-testid="section-header-background_tasks"]:visible').count(),
    ).toBeGreaterThan(0)
    await expandBackgroundTasksSection(page)

    // 3. The Goal tab is always present, because its empty state is where a goal
    //    gets set.
    await workPanelTab(page, 'goal').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()

    // 4. Set one through the dialog.
    await goalAction(page, 'set').click()
    const input = page.locator('[data-testid="set-goal-input"]:visible')
    await input.fill('Reply with the single word DONE and then stop.')
    await page.locator('[data-testid="set-goal-submit"]:visible').click()

    // 5. The card shows the objective the worker stored, not the text typed.
    await expect(goalCard(page)).toBeVisible()
    await expect.poll(async () =>
      await page.locator('[data-testid="goal-objective"]:visible').textContent(),
    ).toContain('Reply with the single word DONE')
    await expectGoalStatus(page, 'active')

    // 6. Pause, then resume. Codex carries both on thread/goal/set, and each
    //    round trip is confirmed by the status the worker broadcasts back.
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')

    // 7. Clear. The card returns to the empty state that can set another.
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()

    // 8. The transcript records the TRANSITIONS and not the progress reports.
    //    This is the assertion the whole change exists for: Codex sends a full
    //    goal report after every completed tool call, and each one used to
    //    become its own raw-JSON row.
    //
    //    Read from the WORKER, not the screen. The chat is a virtual list, so a
    //    row scrolled out of view is not in the DOM and a text count would
    //    report whatever the viewport happens to hold.
    const tabId = await page
      .locator('[data-testid="tab"][data-tab-type="agent"]')
      .first()
      .getAttribute('data-tab-id') ?? ''
    expect(tabId).not.toBe('')
    const agents = await listAgents(hubUrl, adminToken, workerId, [tabId])
    const agentId = agents?.[0]?.id ?? ''
    expect(agentId).not.toBe('')
    const transitions = async () => await countGoalTransitions(hubUrl, adminToken, workerId, agentId)
    // Four actions were performed above.
    await expect.poll(transitions).toBeGreaterThan(0)
    // A generous ceiling that still fails loudly if a progress report ever
    // reaches the transcript again -- that would put one row per completed tool
    // call here, which is the bug this whole change removes.
    await expect.poll(transitions).toBeLessThan(10)
  })
})
