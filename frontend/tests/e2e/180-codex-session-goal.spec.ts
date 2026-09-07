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
  openGoalMenu,
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

    // 4. Set one through the dialog. The field is the app's markdown editor, so
    //    the target is its contenteditable body rather than a textarea.
    await goalAction(page, 'set').click()
    const input = page.locator('[data-testid="goal-editor"]:visible .ProseMirror')
    await input.fill('Reply with the single word DONE and then stop.')
    await page.locator('[data-testid="set-goal-submit"]:visible').click()

    // 5. The card shows the objective the worker stored, not the text typed.
    await expect(goalCard(page)).toBeVisible()
    await expect.poll(async () =>
      await page.locator('[data-testid="goal-objective"]:visible').textContent(),
    ).toContain('Reply with the single word DONE')
    await expectGoalStatus(page, 'active')

    // 6. Replace it through the card's `...` menu, with an objective too long
    //    for the card. The clamp and its disclosure are real LAYOUT -- only a
    //    browser can say whether the box actually hides anything -- so this is
    //    the one place that decision is exercised for real.
    //
    //    Prose, with no markdown marks. `fill` writes into the editor's
    //    contenteditable body without running ProseMirror's input rules, so
    //    typed asterisks would reach the serializer as literal text and come
    //    back escaped. What the card does with real marks is a unit case.
    const longObjective = `Keep going until every check passes on both runners. ${'Then confirm the result and report it back before stopping. '.repeat(8)}`
    await openGoalMenu(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(longObjective)
    await page.locator('[data-testid="set-goal-submit"]:visible').click()

    const objective = page.locator('[data-testid="goal-objective"]:visible')
    await expect.poll(async () => await objective.textContent())
      .toContain('Keep going until every check passes')

    const toggle = page.locator('[data-testid="goal-objective-toggle"]:visible')
    await expect(toggle).toHaveText('Show more')
    const clampedHeight = (await objective.boundingBox())?.height ?? 0
    expect(clampedHeight).toBeGreaterThan(0)
    await toggle.click()
    await expect(toggle).toHaveText('Show less')
    expect((await objective.boundingBox())?.height ?? 0).toBeGreaterThan(clampedHeight)

    // 7. Pause, then resume. Codex carries both on thread/goal/set, and each
    //    round trip is confirmed by the status the worker broadcasts back.
    //    Each verb lives in the card's `...` menu, which closes behind the
    //    click that runs one.
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')

    // 8. Clear. The card returns to the empty state that can set another.
    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()

    // 9. The transcript records the TRANSITIONS and not the progress reports.
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
    // Five actions were performed above.
    await expect.poll(transitions).toBeGreaterThan(0)
    // A generous ceiling that still fails loudly if a progress report ever
    // reaches the transcript again -- that would put one row per completed tool
    // call here, which is the bug this whole change removes.
    await expect.poll(transitions).toBeLessThan(10)
  })

  /**
   * The card is rendered by TWO hosts, and this is the second one: the
   * ThinkingIndicator's goal popover renders the same panel inside a
   * `DropdownMenu as="card"`.
   *
   * The card's `...` menu is therefore a `popover=auto` nested inside another
   * one. A browser that did not treat the inner popover as a descendant of the
   * outer would light-dismiss the card the moment the menu opened, and every
   * verb would be unreachable from this host. Only a real browser can answer
   * that, so it is answered here.
   */
  codexTest('opens the goal actions from the thinking indicator popover', async ({
    authenticatedCodexWorkspace,
    page,
  }) => {
    void authenticatedCodexWorkspace

    // The chip needs a goal to name and a busy agent to sit beside, so the goal
    // is set first from the sidebar, while the agent is idle.
    await sendMessage(page, 'Reply with the single word: ready')
    await waitForAgentIdle(page)
    await expandBackgroundTasksSection(page)
    await workPanelTab(page, 'goal').click()
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill('Keep the build green.')
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expectGoalStatus(page, 'active')

    // A prompt long enough to hold the indicator on screen -- the same device
    // the interrupt spec uses for the same window.
    await sendMessage(page, 'Write a very detailed essay about the history of computing, at least 5000 words across multiple chapters with subheadings.')

    const chip = page.locator('[data-testid="thinking-goal-chip"]:visible')
    await expect(chip).toBeVisible()
    await chip.click()
    const popover = page.locator('[data-testid="goal-popover"]')
    await expect(popover).toBeVisible()

    // Scoped to the popover throughout: the sidebar card is on screen too, so
    // every one of these test ids matches twice while this popover is open.
    await popover.locator('[data-testid="goal-actions-trigger"]').click()
    await expect(popover.locator('[data-testid="goal-action-clear"]')).toBeVisible()
    // The card survived its own menu opening. That is the assertion.
    await expect(popover).toBeVisible()

    // End the turn, so teardown is not racing a running generation.
    await page.keyboard.press('Escape')
    await page.locator('[data-testid="interrupt-button"]').click()
  })
})
