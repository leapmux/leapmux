/**
 * These helpers drive the Goals & To-dos section of the right sidebar in end-to-end (E2E) tests:
 * the session-goal card, its actions and its editor, and the to-do list.
 *
 * The sidebar mounts twice (desktop and mobile), and both mounts can be visible.
 * Scope each locator of a present element to `:visible`.
 * Select the first visible mount for the section header and for the list, because only that mount receives Worker metadata.
 * ./goalsAndTodos.test.ts checks these locator rules in this source.
 */
import type { Locator, Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { countGoalTransitionsInMessages } from './goalTransitions'
import { SCENARIO_MARKER } from './mockModelScript'
import { readAllAgentMessages } from './nativeMessages'
import { composerEditor, expandSidebarSection, queuePauseButton, stableBox } from './ui'

/** Locator for the Goals & To-dos section header in the right sidebar. */
export function goalsAndTodosSection(page: Page): Locator {
  return page.locator('[data-testid="section-header-todos"]:visible').first()
}

/** Locator for the to-do list of the Goals & To-dos section. */
export function goalsAndTodosList(page: Page): Locator {
  return page.locator('[data-testid="goals-and-todos"]:visible').first()
}

/** Expand the Goals & To-dos section if it is collapsed. */
export async function expandGoalsAndTodosSection(page: Page): Promise<void> {
  await expandSidebarSection(goalsAndTodosSection(page))
}

/**
 * Find the visible session-goal card inside Goals & To-dos.
 * Both the sidebar and the ThinkingIndicator popover can show a goal card.
 * Pass a scoped Locator when both surfaces are open.
 */
export function goalCard(page: Page | Locator): Locator {
  return page.locator('[data-testid="goal-card"]:visible')
}

/**
 * Find one goal action, such as set or clear.
 * The empty card displays its set button directly.
 * The other actions require openGoalMenu first.
 * Both the sidebar and the ThinkingIndicator popover can display a goal card.
 * Pass a Locator that selects the intended surface when both are open.
 */
export function goalAction(page: Page | Locator, action: string): Locator {
  return page.locator(`[data-testid="goal-action-${action}"]:visible`)
}

/**
 * Open the existing goal card's action menu.
 * The empty state supplies its set button directly and needs no menu.
 * Use the same scoped root as goalAction when multiple goal surfaces are visible.
 */
export async function openGoalMenu(page: Page | Locator): Promise<void> {
  const trigger = page.locator('[data-testid="goal-actions-trigger"]:visible')
  // Wait for the menu trigger to stop moving before the click.
  // Objective expansion and status changes can move the trigger.
  // A to-do update can also move it.
  // Playwright can otherwise report a timeout even when the moving control remains visible and enabled.
  await stableBox(trigger)
  await trigger.click()
}

/**
 * Type an objective into the Set goal dialog and submit it.
 * The helper expands the section and clicks its set action. The empty card shows that action directly.
 * The field is the app's markdown editor, so the helper fills its contenteditable body rather than a textarea.
 * To replace an existing goal, call openGoalMenu first: the set action then comes from the open menu.
 */
export async function submitGoal(page: Page, objective: string): Promise<void> {
  await expandGoalsAndTodosSection(page)
  await goalAction(page, 'set').click()
  await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
  await page.locator('[data-testid="set-goal-submit"]:visible').click()
}

/** Open the goal menu and clear the goal. A provider that confirms a clear raises its approval after this click. */
export async function clearGoal(page: Page): Promise<void> {
  await openGoalMenu(page)
  await goalAction(page, 'clear').click()
}

/** Require the empty goal card, which shows that the session holds no goal. */
export async function expectEmptyGoalCard(page: Page): Promise<void> {
  await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
}

/** A goal objective that a test types, with the scenario marker that sends its model turns to the test's script. */
export interface GoalObjective {
  /** The text to type into the goal editor. It ends with the scenario marker. */
  input: string
  /** The objective without the marker. */
  text: string
  /** The scenario marker of the test's model script. */
  marker: string
}

/** Build a goal objective whose model turns reach the test's own model script. */
export function scriptedObjective(modelScript: Pick<ModelScript, 'id' | 'prompt'>, text: string): GoalObjective {
  if (text.trim() === '')
    throw new Error('A scripted goal objective needs text.')
  return { input: modelScript.prompt(text), text, marker: `${SCENARIO_MARKER}${modelScript.id}` }
}

/**
 * Require the objective that the goal card displays.
 * A string requires that text. A scripted objective requires its text and its scenario marker.
 */
export async function expectGoalObjective(page: Page, objective: string | GoalObjective): Promise<void> {
  const displayed = page.locator('[data-testid="goal-objective"]:visible')
  if (typeof objective === 'string') {
    await expect(displayed).toContainText(objective)
    return
  }
  await expect(displayed).toContainText(objective.text)
  await expect(displayed).toContainText(objective.marker)
}

/**
 * Wait for the goal card to report the requested status.
 * Worker broadcasts can arrive after the card appears.
 * Poll the status so an earlier rendered state cannot satisfy the assertion.
 */
export async function expectGoalStatus(page: Page, status: string): Promise<void> {
  await expect
    .poll(async () => await page.locator('[data-testid="goal-status-dot"]:visible').getAttribute('data-status'))
    .toBe(status)
}

/**
 * Set a goal through the goal card, and require the objective and the active status.
 * A scripted objective also requires its scenario marker. See expectGoalObjective.
 * `afterSubmit` runs after the submit and before the checks, for a test that must wait for the first goal turn.
 */
export async function setGoal(page: Page, objective: string | GoalObjective, afterSubmit?: () => Promise<void>): Promise<void> {
  await submitGoal(page, typeof objective === 'string' ? objective : objective.input)
  await afterSubmit?.()
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'active')
}

/**
 * Pause the active goal, reload, resume the goal, and clear it.
 * The objective and the paused status must survive the reload.
 * `afterClear` runs between the Clear click and the empty-card check, for a provider that asks to confirm a clear.
 */
export async function pauseResumeClearGoal(
  page: Page,
  objective: string | GoalObjective,
  options: { afterClear?: () => Promise<void> } = {},
): Promise<void> {
  await openGoalMenu(page)
  await goalAction(page, 'pause').click()
  await expectGoalStatus(page, 'paused')

  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  await openGoalMenu(page)
  await goalAction(page, 'resume').click()
  await expectGoalStatus(page, 'active')

  await clearGoal(page)
  await options.afterClear?.()
  await expectEmptyGoalCard(page)
}

/** What one provider's queued goal route looks like on the wire. */
export interface TextGoalQueueCase {
  /** The objective to type into the goal editor. */
  objective: string
  /** The exact command text a Clear must enqueue, such as `/goal off`. */
  clearCommand: string
  /** The composer mode a Set switches the session into, when it switches one. */
  modeAfterSet?: string
  /** The composer mode a Clear restores, when it restores one. */
  modeAfterClear?: string
}

/**
 * Verify the provider's real queued goal command.
 * Pause the queue to establish the state before native delivery.
 * The options object distinguishes the two optional mode values.
 * Positional strings could swap those values without a type error and fail later inside this helper.
 */
export async function exerciseTextGoalQueue(page: Page, test: TextGoalQueueCase): Promise<void> {
  const queue = page.locator('[data-testid="agent-input-queue"]:visible')
  const pauseButton = queuePauseButton(page)
  const modeTrigger = page.locator('[data-testid="composer-mode-trigger"]:visible')

  await expect(composerEditor(page)).toBeVisible()
  await expect(goalsAndTodosSection(page)).toBeVisible()
  await expandGoalsAndTodosSection(page)
  await expect(goalAction(page, 'set')).toBeVisible()

  await pauseButton.click()
  await submitGoal(page, test.objective)
  await expect(queue).toContainText(`/goal ${test.objective}`)
  // The command stays in the queue until the provider receives it.
  // Require the empty goal card before that delivery.
  await expectEmptyGoalCard(page)

  await pauseButton.click()
  await expectGoalObjective(page, test.objective)
  if (test.modeAfterSet)
    await expect(modeTrigger).toContainText(test.modeAfterSet)

  // Pause again so the clear command stays visible in the queue while the goal
  // turn changes state.
  await pauseButton.click()
  await clearGoal(page)
  await expect(queue).toContainText(test.clearCommand)

  // The set command starts a turn that prevents the clear command from dispatching.
  // End that turn before releasing the clear command.
  // A single interrupt-button count can race a turn end and cannot prove that the queue drained.
  // A turn end between that count and the click can also detach the button.
  await endActiveTurn(page)
  await pauseButton.click()
  await expect(queue).toHaveCount(0)
  await expectEmptyGoalCard(page)
  if (test.modeAfterClear)
    await expect(modeTrigger).toContainText(test.modeAfterClear)
}

/**
 * End the active turn if one exists.
 * Try the click directly because a prior count can race the turn end.
 * Ignore that click failure, then require the interrupt button to disappear.
 * The final assertion proves that the turn ended even when the click races it.
 */
async function endActiveTurn(page: Page): Promise<void> {
  const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
  await interrupt.click().catch(() => {})
  await expect(interrupt).toHaveCount(0)
}

/**
 * Count the persisted session-goal transitions of one agent through the encrypted Worker test channel.
 * A virtual chat list removes rows outside its viewport, so a count of rendered text cannot count the whole transcript.
 * The count reads every stored page. It returns null while a Worker read fails, so a caller can poll through a reconnection.
 */
export async function countGoalTransitions(
  context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>,
  agentId: string,
): Promise<number | null> {
  if (agentId.trim() === '')
    throw new Error('The goal transition count requires an agent ID.')
  try {
    return countGoalTransitionsInMessages(await readAllAgentMessages(context, agentId))
  }
  catch {
    return null
  }
}
