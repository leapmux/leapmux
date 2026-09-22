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
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { updateTodosToolCall } from './helpers/providerToolCalls'
import {
  countGoalTransitions,
  expandGoalsAndTodosSection,
  expectGoalStatus,
  goalAction,
  goalCard,
  listAgents,
  openGoalMenu,
} from './helpers/subagentRegistry'
import { sendMessage, stableBox, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex session goal', () => {
  codexTest('offers Steer for input queued during a goal turn', async ({
    authenticatedCodexWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedCodexWorkspace

    // Start the process before the side-band goal command asks Codex to start
    // its own turn. That turn has no queue input to supply its classification.
    await modelScript.queue({ text: 'ready' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)
    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    // The objective carries the marker because CODEX starts the next turn
    // itself, with the objective as its prompt: an unmarked goal reaches the
    // ambient scenario, which refuses it. The goal turn must still be RUNNING
    // when the steer arrives, so its answer is held open.
    await modelScript.fallback({ text: 'Working on the objective.', delayMs: 120_000 })
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(
      modelScript.prompt('Inspect this repository until I send a steering message. Do not stop before that message.'),
    )
    await page.locator('[data-testid="set-goal-submit"]:visible').click()

    // The Interrupt button proves that the provider-started goal turn runs.
    // Send while that condition still holds, so the message enters the queue.
    await expect(page.getByTestId('interrupt-button')).toBeVisible()
    await sendMessage(page, modelScript.prompt('Stop now, mark the goal complete, and reply with STEERED.'))
    const queued = page.getByTestId(/^queued-input-/).filter({ hasText: 'Stop now' })
    await expect(queued).toBeVisible()

    const steer = queued.getByRole('button', { name: 'Steer' })
    await expect(steer).toBeVisible()
    await steer.click()
    await expect(queued).toHaveCount(0)
  })

  codexTest('set a goal from the panel, pause it, resume it, and clear it', async ({
    authenticatedCodexWorkspace,
    page,
    leapmuxServer,
    modelScript,
  }) => {
    void authenticatedCodexWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // 1. Drive one turn first. It puts the agent tab on screen and, more to the
    //    point, gets the process registered -- the goal's supported ACTIONS are
    //    read from the running agent, and everything below depends on them.
    await modelScript.queue({ text: 'ready' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)

    // Codex starts a turn of its OWN on every goal set and resume, with the
    // objective as the prompt. This test performs five such actions, and how
    // many turns each one costs belongs to the provider -- so a fallback
    // answers them all. Each objective below is marked for the same reason the
    // prompts are: an unmarked one reaches the ambient scenario instead.
    //
    // The DELAY is load-bearing. An active goal keeps Codex starting turns, and
    // a fallback that answers in microseconds turns that into a loop running at
    // mock speed: the run allocated 4 GB of request bodies and killed the
    // Playwright worker outright. One second a turn is what a live model costs
    // anyway, and it keeps the count to what this test actually exercises.
    await modelScript.fallback({ text: 'DONE', delayMs: 1000 })

    // 2. The section is reachable with no to-dos or background tasks. The
    //    provider feature keeps the empty goal card visible.
    await expect.poll(async () =>
      await page.locator('[data-testid="section-header-todos"]:visible').count(),
    ).toBeGreaterThan(0)
    await expandGoalsAndTodosSection(page)

    // 3. The empty card is the route to the first goal.
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()

    // 4. Set one through the dialog. The field is the app's markdown editor, so
    //    the target is its contenteditable body rather than a textarea.
    await goalAction(page, 'set').click()
    const input = page.locator('[data-testid="goal-editor"]:visible .ProseMirror')
    await input.fill(modelScript.prompt('Reply with the single word DONE and then stop.'))
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
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt(longObjective))
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
   * Two hosts render the card. The ThinkingIndicator to-dos popover renders
   * the merged section inside a `DropdownMenu as="card"`.
   *
   * The card's `...` menu is therefore a `popover=auto` nested inside another
   * one. A browser that did not treat the inner popover as a descendant of the
   * outer would light-dismiss the card the moment the menu opened, and every
   * verb would be unreachable from this host. Only a real browser can answer
   * that, so it is answered here.
   */
  codexTest('opens the goal actions from the to-dos popover', async ({
    authenticatedCodexWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedCodexWorkspace

    // Set the goal from the sidebar while the agent is idle.
    await modelScript.queue({ text: 'ready' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)
    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    // Marked, and answered by a fallback, because Codex starts its own turn on
    // every goal set with the objective as the prompt.
    await modelScript.fallback({ text: 'Working on the objective.' })
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Keep the build green.'))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expectGoalStatus(page, 'active')

    // The to-do list is SCRIPTED, so the chip below is a precondition this test
    // establishes rather than one it hopes for. A real model answered prose as
    // often as a plan, which is why the chip lookup still carries a skip.
    await modelScript.queue({
      toolCalls: [updateTodosToolCall(AgentProvider.CODEX, 'plan-1', [
        { step: 'Inspect the repository', status: 'completed' },
        { step: 'List three checks', status: 'in_progress' },
        { step: 'Report their purpose', status: 'pending' },
      ])],
    })
    await sendMessage(page, modelScript.prompt('Create and execute a multi-step plan to inspect this repository, list three checks, and report their purpose.'))

    // HOLD the next turn open. The goal is active, so Codex starts one turn
    // after another; each one re-renders the thinking indicator, and the chip
    // below lives INSIDE it. A popover opened from that chip is detached by the
    // very next re-render -- it reports `hidden` while the element is still in
    // the DOM, which reads like a popover that refused to open.
    //
    // One long turn is also what this test means by "while the indicator remains
    // visible": with a live model that turn took seconds on its own.
    await modelScript.fallback({ text: 'Still working on the objective.', delayMs: 60_000 })

    // The chip is the only route to this popover. It used to appear only when a
    // real model chose to emit a plan, so a run that answered prose skipped the
    // whole nested-popover case -- and nothing else covers a `popover=auto`
    // inside a `DropdownMenu as="card"`. The to-do list above is scripted, so
    // the chip is now a guarantee and its absence is a failure.
    const chip = page.locator('[data-testid="thinking-todos-chip"]:visible')
    await expect(chip).toBeVisible()
    await chip.click()
    const popover = page.locator('[data-testid="todo-list-popover"]')
    await expect(popover).toBeVisible()
    // Settle before clicking anything inside it: the list renders over several
    // frames, each growth re-anchors the card, and a trigger that is still
    // moving is one Playwright refuses to click as "not stable".
    await stableBox(popover)
    // The goal rides this popover now, so its presence is part of the contract.
    await expect(goalCard(popover)).toBeVisible()

    // Rooted at the popover, through the same helpers the sidebar cases use:
    // the sidebar card is on screen too, so every one of these test ids matches
    // twice while this popover is open, and only the root separates them.
    await openGoalMenu(popover)
    await expect(goalAction(popover, 'clear')).toBeVisible()
    // The card survived its own menu opening. That is the assertion.
    await expect(popover).toBeVisible()

    // End the turn, so teardown is not racing a running generation.
    await page.keyboard.press('Escape')
    await page.locator('[data-testid="interrupt-button"]').click()
  })
})
