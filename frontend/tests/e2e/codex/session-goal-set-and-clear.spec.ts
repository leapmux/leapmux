import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { clearGoal, countGoalTransitions, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, goalCard, goalObjective, goalsAndTodosSection, openGoalMenu, submitGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeAgentById, selectedAgentTabId } from '../helpers/nativeScenario'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { initialTodoList, TODO_LIST_STEPS } from '../helpers/todoSidebar'
import { sendMessage, stableBox, transcriptRows } from '../helpers/ui'

/** Test acknowledged native goal commands and the Worker goal state. */
codexTest.describe('Codex session goal', () => {
  codexTest('set a goal from the panel, pause it, resume it, and clear it', async ({ native }) => {
    const { page, modelScript } = native

    // 1. Drive one turn first. It puts the agent tab on screen and, more
    //    importantly, registers the process. The goal's supported ACTIONS come
    //    from the running agent, and everything below depends on them.
    await sendNativeAnswer(native, 'Reply with the single word: ready', 'ready')

    // Codex starts a turn of its OWN on every goal set and resume, with the
    // objective as the prompt. This test performs five such actions, and the
    // count of turns that each one starts belongs to the provider. So a fallback
    // answers them all. Each objective below carries the scenario marker for the
    // same reason as the prompts: an unmarked one reaches the ambient scenario instead.
    //
    // The DELAY is necessary. An active goal keeps Codex starting turns, and a
    // fallback that answers in microseconds turns that into a loop at mock speed:
    // the run allocated 4 GB of request bodies and killed the Playwright worker.
    // One second a turn is what a live model costs anyway, and it keeps the
    // count to what this test actually exercises.
    await modelScript.fallback({ text: 'DONE', delayMs: 1000 })

    // 2. The section is reachable with no to-dos or background tasks. The
    //    provider feature keeps the empty goal card visible.
    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)

    // 3. The empty card is the route to the first goal.
    await expectEmptyGoalCard(page)

    // 4. Set one through the dialog.
    await submitGoal(page, modelScript.prompt('Reply with the single word DONE and then stop.'))

    // 5. The card shows the objective that the worker stored, not the text typed.
    await expect(goalCard(page)).toBeVisible()
    await expectGoalObjective(page, 'Reply with the single word DONE')
    await expectGoalStatus(page, 'active')

    // 6. Replace it through the card's `...` menu, with an objective too long
    //    for the card. The clamp and its disclosure are real LAYOUT. Only a
    //    browser can say whether the box actually hides anything, so this is
    //    the one place that exercises that decision for real.
    //
    //    Prose, with no markdown marks. `fill` writes into the editor's
    //    contenteditable body without running ProseMirror's input rules, so
    //    typed asterisks would reach the serializer as literal text and come
    //    back escaped. A unit case covers what the card does with real marks.
    const longObjective = `Keep going until every check passes on both runners. ${'Then confirm the result and report it back before stopping. '.repeat(8)}`
    await openGoalMenu(page)
    await submitGoal(page, modelScript.prompt(longObjective))

    const objective = goalObjective(page)
    await expectGoalObjective(page, 'Keep going until every check passes')

    const toggle = page.locator('[data-testid="goal-objective-toggle"]:visible')
    await expect(toggle).toHaveText('Show more')
    const clampedHeight = (await objective.boundingBox())?.height ?? 0
    expect(clampedHeight).toBeGreaterThan(0)
    await toggle.click()
    await expect(toggle).toHaveText('Show less')
    expect((await objective.boundingBox())?.height ?? 0).toBeGreaterThan(clampedHeight)

    // 7. Pause, then resume. Codex carries both on thread/goal/set, and the
    //    status that the worker broadcasts back confirms each round trip.
    //    Each verb is in the card's `...` menu, which closes behind the
    //    click that runs one.
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')

    // 8. Clear. The card returns to the empty state that can set another.
    await clearGoal(page)
    await expectEmptyGoalCard(page)

    // 9. The transcript records the TRANSITIONS and not the progress reports.
    //    The whole change exists for this assertion: Codex sends a full goal
    //    report after every completed tool call, and each one used to become
    //    its own raw-JSON row.
    //
    //    Read from the WORKER, not the screen. The chat is a virtual list, so a
    //    row scrolled out of view is not in the DOM, and a text count would
    //    report only the rows that the viewport holds.
    const agentId = (await nativeAgentById(native, await selectedAgentTabId(page)))?.id ?? ''
    expect(agentId).not.toBe('')
    const transitions = async () => await countGoalTransitions(native, agentId)
    // The steps above performed five actions.
    await expect.poll(transitions).toBeGreaterThan(0)
    // A generous ceiling that still fails clearly if a progress report
    // reaches the transcript again. That would put one row for each completed
    // tool call here, which is the bug that this whole change removes.
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
   * that, so this test answers it.
   */
  codexTest('opens the goal actions from the to-dos popover', async ({ native }) => {
    const { page, modelScript } = native

    // Set the goal from the sidebar while the agent is idle.
    await sendNativeAnswer(native, 'Reply with the single word: ready', 'ready')
    // Marked, and answered by a fallback, because Codex starts its own turn on
    // every goal set with the objective as the prompt.
    await modelScript.fallback({ text: 'Working on the objective.' })
    await submitGoal(page, modelScript.prompt('Keep the build green.'))
    await expectGoalStatus(page, 'active')

    // The to-do list is SCRIPTED, so the chip below is a precondition that this
    // test establishes, not one that it hopes for. A real model answered prose as
    // often as a plan. Because of this step, the chip lookup below asserts and
    // does not skip.
    await modelScript.queue({
      toolCalls: [updateTodosToolCall(AgentProvider.CODEX, 'plan-1', initialTodoList(TODO_LIST_STEPS))],
    })

    // HOLD the next turn open. The goal is active, so Codex starts one turn
    // after another. Each one renders the thinking indicator again, and the chip
    // below is INSIDE it. The next render detaches a popover opened from that
    // chip. The popover then reports `hidden` while the element is still in
    // the DOM, which looks like a popover that refused to open. A detached
    // popover never opens again, so the assertion then waits out its whole timeout.
    //
    // The test registers the hold BEFORE the send, and that order is the fix. A
    // later fallback replaces the goal-set one above, which answers at once.
    // Registered after `sendMessage`, the hold arrived too late: a recorded run
    // shows the plan step consumed and then SEVEN more turns answered by the fast
    // fallback inside thirty seconds, each one rendering the indicator under the
    // chip again. Here the turn after the plan parks, whether the goal loop or
    // the send starts it first, so the indicator holds still.
    //
    // The queue must come first. Registered before it, the hold would park the
    // goal loop's next turn with nothing to consume, and the plan step would
    // wait sixty seconds for a turn to take it.
    //
    // One long turn is also what this test means by "while the indicator remains
    // visible": with a live model that turn took seconds on its own.
    await modelScript.fallback({ text: 'Still working on the objective.', delayMs: 60_000 })
    await sendMessage(page, modelScript.prompt('Create and execute a multi-step plan to inspect this repository, list three checks, and report their purpose.'))

    // The chip is the only route to this popover. It used to appear only when a
    // real model chose to emit a plan, so a run that answered prose skipped the
    // whole nested-popover case, and nothing else covers a `popover=auto`
    // inside a `DropdownMenu as="card"`. The to-do list above is scripted, so
    // the chip is now a guarantee and its absence is a failure.
    const chip = page.locator('[data-testid="thinking-todos-chip"]:visible')
    await expect(chip).toBeVisible()
    // The chip shows before the transcript finishes the plan rows. The transcript hides the row
    // that holds the plan, measures it again, and reveals it. The reveal moves the thinking
    // indicator and its chip down by about 35px. Playwright checks the click target at the first
    // pointer event only. A click that crosses the move releases the pointer on another element,
    // so the browser sends no click to the chip. Both failed traces show the reveal inside the
    // click window and a chip that never toggled: `aria-expanded` stays `false` and the popover
    // never positions itself. Wait until the result row of the plan shows and the chip stops
    // moving. The plan row itself shows earlier, because its source code lists every step. The
    // transcript reveals rows in order, so the result row shows after the plan row.
    await expect(transcriptRows(page).filter({ hasText: '[no output]' }).first()).toBeVisible()
    await chip.scrollIntoViewIfNeeded()
    await stableBox(chip)
    await chip.click()
    const popover = page.locator('[data-testid="todo-list-popover"]')
    await expect(popover).toBeVisible()
    // Settle before clicking anything inside it: the list renders over several
    // frames, each growth re-anchors the card, and Playwright refuses to click
    // a trigger that still moves, as "not stable".
    await stableBox(popover)
    // The goal is in this popover now, so its presence is part of the contract.
    await expect(goalCard(popover)).toBeVisible()

    // Rooted at the popover, through the same helpers that the sidebar cases use:
    // the sidebar card is on screen too, so each of these test ids matches
    // twice while this popover is open, and only the root separates them.
    await openGoalMenu(popover)
    await expect(goalAction(popover, 'clear')).toBeVisible()
    // The card survived its own menu opening. That is the assertion.
    await expect(popover).toBeVisible()

    // End the turn, so teardown does not race a running generation.
    await page.keyboard.press('Escape')
    await page.locator('[data-testid="interrupt-button"]:visible').click()
  })
})
