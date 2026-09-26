/**
 * Shared helpers for the end-to-end specs that drive the background-task
 * registry and the Goals & To-dos section.
 *
 * These wrap the common registry assertions so each per-provider spec stays
 * small. A locator that must MATCH an element is `:visible`-scoped, because the
 * chat rows and the sidebar both render twice; a locator that asserts a count of
 * ZERO is not, because `:visible` also reads zero for a collapsed section. All
 * worker-state reads go through the end-to-end encrypted test channel. They do
 * not use optimistic conflict-free replicated data type (CRDT) state.
 * Playwright's global expect timeout applies to each call. The config is in
 * `playwright.config.ts`. `./subagentRegistry.test.ts` holds both halves
 * of the `:visible` rule as a source-level guard.
 *
 * The stop of a subagent from its own tab starts at a registry row too, so its
 * shared scenario (`exerciseChildInterrupt`) lives here as well.
 */
import type { Locator, Page } from '@playwright/test'
import type { AgentInfo, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelMatcher, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, ListAgentsRequestSchema, ListAgentsResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect } from '../fixtures'
import { getTestChannel } from './api'
import { countGoalTransitionsInMessages } from './goalTransitions'
import { MAX_STEP_DELAY_MS } from './mockModelScript'
import { spawnSubagentToolCall } from './providerToolCalls'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  assistantBubbles,
  expectAssistantAnswer,
  messageBubbles,
  sendMessage,
  stableBox,
  tabById,
} from './ui'

const FINAL_STATUSES = ['completed', 'failed', 'stopped', 'interrupted'] as const

/** Locator for the Background tasks section header (right sidebar). */
export function backgroundTasksSection(page: Page): Locator {
  // `:visible`-scoped like every other locator in this file: the sidebar is
  // mounted twice (the desktop and the mobile tree both render), so the bare
  // test id matches two elements and every strict-mode call on it throws once
  // the section exists.
  return page.locator('[data-testid="section-header-background_tasks"]:visible')
}

/** Locator for the Goals & To-dos section header in the right sidebar. */
export function goalsAndTodosSection(page: Page): Locator {
  return page.locator('[data-testid="section-header-todos"]:visible')
}

async function expandSection(section: Locator): Promise<void> {
  const isOpen = await section.evaluate(el => !el.hasAttribute('data-closed')).catch(() => true)
  if (!isOpen)
    await section.locator('> [role="button"]').click()
}

/** Expand the Background tasks section if it is collapsed. */
export async function expandBackgroundTasksSection(page: Page): Promise<void> {
  await expandSection(backgroundTasksSection(page))
}

/** Expand the Goals & To-dos section if it is collapsed. */
export async function expandGoalsAndTodosSection(page: Page): Promise<void> {
  await expandSection(goalsAndTodosSection(page))
}

/**
 * The session-goal card inside Goals & To-dos.
 *
 * `:visible`-scoped for the reason every locator in this file is: the sidebar is
 * mounted twice, so the bare test id matches two elements.
 */
export function goalCard(page: Page | Locator): Locator {
  return page.locator('[data-testid="goal-card"]:visible')
}

/**
 * A verb on the goal card, by action (`set`, `clear`, `pause`, `resume`).
 *
 * Every verb but the empty state's own `set` lives inside the card's `...`
 * menu, so a caller opens that menu first -- see `openGoalMenu`.
 *
 * `page` takes a `Locator` as well. A caller that can see two goal cards must
 * pass one. The sidebar card and the ThinkingIndicator popover card are
 * both on screen while that popover is open, so `:visible` alone resolves two
 * elements and Playwright's strict mode fails the call. Rooting the search at
 * the popover is the only thing that separates them.
 */
export function goalAction(page: Page | Locator, action: string): Locator {
  return page.locator(`[data-testid="goal-action-${action}"]:visible`)
}

/**
 * Open the goal card's `...` menu, which holds every verb for a goal that
 * already exists.
 *
 * The empty state is the exception: `set` is the only verb that applies with no
 * goal, and the card offers it there as its own button.
 *
 * Takes the same root as `goalAction`, and for the same reason.
 */
export async function openGoalMenu(page: Page | Locator): Promise<void> {
  const trigger = page.locator('[data-testid="goal-actions-trigger"]:visible')
  // SETTLE first. The card this trigger sits in relayouts around it -- the
  // objective expands from its clamp, the status line swaps, a to-do list grows
  // beneath it -- and a trigger that is still moving is one Playwright scrolls
  // to, finds stable, and then fails to click as the box shifts underneath.
  // The failure names only a timeout on a visible, enabled, stable element,
  // which reads like a covered button rather than a moving one.
  await stableBox(trigger)
  await trigger.click()
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
 * Verify one provider's queued text route against its real command-line
 * interface. The paused queue makes the state before delivery deterministic.
 *
 * One options object rather than four strings. The four are all strings, and
 * the two optional ones are adjacent and describe the same kind of value, so a
 * positional call that transposed them compiled and failed later as an opaque
 * timeout inside this helper instead of at the call site.
 */
export async function exerciseTextGoalQueue(page: Page, test: TextGoalQueueCase): Promise<void> {
  const queue = page.locator('[data-testid="agent-input-queue"]:visible')
  const pauseButton = page.locator('[data-testid="queue-pause-button"]:visible')
  const modeTrigger = page.locator('[data-testid="composer-mode-trigger"]:visible')

  await expect(page.locator('[data-testid="composer-editor"]:visible .ProseMirror')).toBeVisible()
  await expect(goalsAndTodosSection(page)).toBeVisible()
  await expandGoalsAndTodosSection(page)
  await expect(goalAction(page, 'set')).toBeVisible()

  await pauseButton.click()
  await goalAction(page, 'set').click()
  await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(test.objective)
  await page.locator('[data-testid="set-goal-submit"]:visible').click()
  await expect(queue).toContainText(`/goal ${test.objective}`)
  // The card must still be EMPTY: the command sits in the queue, and the goal
  // row changes only once the provider takes it. This is the whole point of the
  // durable route, so it is asserted rather than assumed.
  await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()

  await pauseButton.click()
  await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(test.objective)
  if (test.modeAfterSet)
    await expect(modeTrigger).toContainText(test.modeAfterSet)

  // Pause again so the clear command stays visible in the queue while the goal
  // turn changes state.
  await pauseButton.click()
  await openGoalMenu(page)
  await goalAction(page, 'clear').click()
  await expect(queue).toContainText(test.clearCommand)

  // The set started a turn, and the clear cannot dispatch behind it. End the
  // turn deterministically rather than reading a one-shot count of the
  // interrupt button: that count answered differently on every run, so a real
  // failure to drain the clear was indistinguishable from a run where the turn
  // had already ended -- and a turn that ended between the count and the click
  // failed the click on a detached element.
  await endActiveTurn(page)
  await pauseButton.click()
  await expect(queue).toHaveCount(0)
  await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  if (test.modeAfterClear)
    await expect(modeTrigger).toContainText(test.modeAfterClear)
}

/**
 * End the active turn, whether or not one is running.
 *
 * `click()` on a control that is not there fails, and a `count()` first is the
 * race this exists to avoid, so the click is attempted and its failure
 * discarded. The assertion that follows is what proves the turn ended: the
 * interrupt button is present exactly while one runs.
 */
async function endActiveTurn(page: Page): Promise<void> {
  const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
  await interrupt.click().catch(() => {})
  await expect(interrupt).toHaveCount(0)
}

/**
 * Wait for the goal card to report a status.
 *
 * Polls rather than asserting once: the status is worker state that arrives on a
 * broadcast, so the card can be on screen before the status it will settle on
 * is.
 */
export async function expectGoalStatus(page: Page, status: string): Promise<void> {
  await expect
    .poll(async () => await page.locator('[data-testid="goal-status-dot"]:visible').getAttribute('data-status'))
    .toBe(status)
}

/**
 * Verify the registry holds no ROWS, and reports no load failure, before a
 * spawn.
 *
 * The load-failure assertion is what the row count alone loses. A worker that
 * cannot answer for the registry renders the failure message with ZERO rows, so
 * a row count of nothing passes in exactly the state the old section assertion
 * caught. Without it a schema-drifted worker database reaches the post-spawn
 * step and fails there as "the model did not spawn a subagent" -- a skip, not a
 * failure -- and the real regression never surfaces.
 *
 * NOT `:visible`-scoped, unlike the row locators below. A count of zero is
 * already immune to the double mount, and the bare test id also fails when the
 * section is COLLAPSED, where `:visible` would match nothing and pass for the
 * wrong reason.
 *
 * Waits a beat first, so a stale row that leaked in from a previous test fails
 * fast rather than passing before the initial broadcast lands.
 */
export async function expectNoRegistryRows(page: Page): Promise<void> {
  // Wait a beat for the initial registry broadcast to settle, then assert.
  await page.waitForTimeout(1000)
  await expect(
    page.locator('[data-testid="bg-task-row"]'),
    'the registry should hold no rows before a spawn',
  ).toHaveCount(0)
  await expect(
    page.locator('[data-testid="bg-task-load-failed"]'),
    'the worker should be able to answer for the registry',
  ).toHaveCount(0)
}

export interface RowFilter {
  kind?: 'subagent' | 'shell'
  status?: string
  titleContains?: string
}

/**
 * Poll for a registry subagent row to appear and return it. Unlike
 * waitForAgentIdle (which now blocks while background tasks are active, since
 * the thinking indicator stays up for an active task count), this waits only
 * for the row itself -- the observable we actually want after a spawn.
 */
export async function waitForRegistryRow(page: Page, kind: 'subagent' | 'shell' = 'subagent'): Promise<Locator> {
  await expect.poll(async () => {
    await expandBackgroundTasksSection(page)
    return backgroundTasksSection(page).isVisible()
  }).toBe(true)
  const row = page.locator(`[data-testid="bg-task-row"]:visible[data-kind="${kind}"]`).first()
  await expect(row).toBeVisible()
  return row
}

/**
 * The row locator once a registry row appears, or null once the wait expires.
 *
 * This exists so `requireRegistryRow` can turn an expired wait into a named
 * assertion rather than a bare timeout. It is not a licence to continue without
 * a row: every caller scripts the spawn, so a null is a defect.
 */
async function tryWaitForRegistryRow(page: Page, kind: 'subagent' | 'shell' = 'subagent'): Promise<Locator | null> {
  const row = page.locator(`[data-testid="bg-task-row"]:visible[data-kind="${kind}"]`).first()
  try {
    await expect.poll(async () => {
      await expandBackgroundTasksSection(page)
      return row.isVisible()
    }).toBe(true)
    return row
  }
  catch {
    return null
  }
}

/**
 * Wait for a registry row, and FAIL the spec when none appears.
 *
 * This used to skip instead, because a real model could decline to spawn. Every
 * caller scripts the spawn against the mock now, so a missing row is a defect
 * in the product or in the script -- and a skip hid exactly that for six specs
 * at once. The helper returns a non-null row, so the caller needs no `!`.
 */
export async function requireRegistryRow(
  page: Page,
  kind: 'subagent' | 'shell' = 'subagent',
): Promise<Locator> {
  const row = await tryWaitForRegistryRow(page, kind)
  expect(row, kind === 'shell'
    ? 'the scripted command produced no shell row in the registry'
    : 'the scripted spawn produced no subagent row in the registry').not.toBeNull()
  return row!
}

/**
 * Open a subagent's transcript from its sidebar row and return the new tab's id.
 *
 * Reading the ids of the agent tabs, clicking, asserting the count grew by one,
 * and taking the one id that is new is the same sequence in every spec that
 * opens a child, and the id must come from the rendered tab strip -- the hub's
 * tab list is empty throughout these runs. The new id is the one that the strip
 * did not hold, not the one at a given position: a child tab opens beside its
 * parent, so a second child lands before the first.
 */
export async function openChildTabFromRow(page: Page, row: Locator): Promise<string> {
  const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
  const tabIds = async () => agentTabs.evaluateAll(tabs => tabs.map(tab => tab.getAttribute('data-tab-id') ?? ''))
  const before = new Set(await tabIds())
  await row.click()
  await expect(agentTabs).toHaveCount(before.size + 1)
  const added = (await tabIds()).filter(id => !before.has(id))
  expect(added, 'one new agent tab').toHaveLength(1)
  expect(added[0]).not.toBe('')
  return added[0]!
}

/**
 * The task of a subagent whose turn runs until something stops it. A
 * `childTurn` matcher selects on these words.
 */
export const HELD_CHILD_TASK = 'Count slowly to one hundred'

/** The description of that subagent, which its registry row shows. */
const HELD_CHILD_TITLE = 'Count to one hundred'

/** The name of the rule that holds the child's turn open. */
const HELD_CHILD_RULE = 'the child counts until something stops it'

/** What one provider needs to open the tab of a subagent that keeps working. */
export interface HeldChildCase {
  provider: AgentProvider
  /**
   * The matcher that selects the child's OWN model turn, and no other request.
   * It must select on {@link HELD_CHILD_TASK}.
   *
   * The script holds every request that it selects. A test rule precedes the
   * housekeeping rules, so a matcher that also selects the session title that
   * a child asks for holds that request as well, and the held rule then
   * reports two matches for one child.
   */
  childTurn: MockModelMatcher
  /** The root's turns after the turn that spawns the child, in order. */
  rootTurnsAfterSpawn: MockModelStep[]
}

/** The subagent that {@link openHeldChildTab} leaves working. */
export interface HeldChild {
  /** Its registry row. */
  row: Locator
  /** The id of the child's tab, which is the child agent's id. */
  childTabId: string
  /** The id of the root's tab, which the child's tab opened beside. */
  rootTabId: string
  /** How many requests the rule that holds the child's turn answered. */
  heldTurns: () => Promise<number>
}

/**
 * Spawn a subagent whose model turn stays open, and open its tab.
 *
 * The mock holds the child's answer for as long as it permits, which is as long
 * as the test timeout, so only a stop ends the child's turn inside a test. When
 * this returns, the child's model request is open, its row is running, and its
 * tab is the active tab.
 */
export async function openHeldChildTab(page: Page, modelScript: ModelScript, test: HeldChildCase): Promise<HeldChild> {
  await modelScript.rule({
    name: HELD_CHILD_RULE,
    when: test.childTurn,
    respond: { text: 'One, two, three.', delayMs: MAX_STEP_DELAY_MS },
  })
  await modelScript.queue(
    {
      toolCalls: [spawnSubagentToolCall(test.provider, 'spawn-held-child', {
        description: HELD_CHILD_TITLE,
        prompt: modelScript.prompt(`${HELD_CHILD_TASK}.`),
      })],
    },
    ...test.rootTurnsAfterSpawn,
  )
  // The root's tab is the only agent tab before the spawn. The hub's tab list
  // is empty throughout these runs, so the id comes from the rendered strip.
  const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
  await expect(agentTabs).toHaveCount(1)
  const rootTabId = await agentTabs.getAttribute('data-tab-id') ?? ''
  expect(rootTabId, 'the root tab has an id').not.toBe('')

  await sendMessage(page, modelScript.prompt('Delegate the count to a subagent.'))
  await modelScript.waitForSteps(1)
  const row = await requireRegistryRow(page)
  await expect(row).toContainText(HELD_CHILD_TITLE)
  await expect(row).toHaveAttribute('data-status', 'running')
  const heldTurns = async () => (await modelScript.status()).ruleMatches[HELD_CHILD_RULE] ?? 0
  // The child asked the model for its turn, and the held answer keeps that
  // request open: whatever ends the turn now ends it in the middle.
  await expect.poll(heldTurns).toBe(1)
  await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
  const childTabId = await openChildTabFromRow(page, row)
  return { row, childTabId, rootTabId, heldTurns }
}

/** The root's answer after the spawn's result states the stop. */
const ROOT_AFTER_CHILD_STOP = 'The subagent stopped before it finished.'

/** What one provider needs for {@link exerciseChildInterrupt}. */
export type ChildInterruptCase = Omit<HeldChildCase, 'rootTurnsAfterSpawn'> & {
  /**
   * The word the closing divider uses. Defaults to `interrupted`.
   *
   * Every provider that stops one subagent through `InterruptChild` reports a
   * user interrupt as its own outcome, so the row closes as `interrupted` and
   * the divider reads "Subagent interrupted". A provider that reports a plain
   * stop instead states `stopped` here.
   */
  finalStatus?: 'stopped' | 'interrupted'
}

/**
 * Stop a working subagent through the Interrupt control of its own tab, and
 * prove that the stop ends the child's turn alone.
 *
 * The control is on the child's tab only when the worker states that the
 * provider can stop one subagent (`AgentInfo.accepts_interrupt`), and the
 * press reaches the provider through the worker's `InterruptChild`. The user
 * sees each of these:
 *
 * - The child's tab offers Interrupt while the child works.
 * - After the press, the child's row and its transcript state a stop, and the
 *   control goes away.
 * - The root goes on with its own turn, and takes the next prompt.
 */
export async function exerciseChildInterrupt(page: Page, modelScript: ModelScript, test: ChildInterruptCase): Promise<void> {
  const finalStatus = test.finalStatus ?? 'interrupted'
  const child = await openHeldChildTab(page, modelScript, { ...test, rootTurnsAfterSpawn: [{ text: ROOT_AFTER_CHILD_STOP }] })

  const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
  await expect(interrupt).toBeVisible()
  await interrupt.click()

  // A stop, not a failure and not a completion: the held answer never arrived.
  // The divider word matches the registry status, so a provider that reports a
  // user interrupt as `interrupted` draws "Subagent interrupted".
  await expect(child.row).toHaveAttribute('data-status', finalStatus)
  await expect(messageBubbles(page).filter({ hasText: `Subagent ${finalStatus}` })).toBeVisible()
  await expect(interrupt).toHaveCount(0)

  // The root reads the spawn's result and answers with its next turn.
  await modelScript.waitForSteps()
  await tabById(page, child.rootTabId).click()
  await expect(assistantBubbles(page).filter({ hasText: ROOT_AFTER_CHILD_STOP })).toBeVisible()
  // The root's turn ends. Not `waitForAgentIdle`: each tab of a tile mounts its
  // own chat, and the child's hidden chat keeps its indicator, so an unscoped
  // indicator locator matches two elements and fails in strict mode.
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  // The stop paused the child's input queue, not the root's, so the next prompt
  // reaches the root at once.
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await expectAssistantAnswer(page)
  // Nothing asked for the child's turn again after the stop.
  expect(await child.heldTurns()).toBe(1)
}

/**
 * Resolve a visible registry row matching the filter. Returns the row locator.
 * Throws (via expect) if no match is found within the default timeout.
 */
export async function expectRegistryRow(page: Page, filter: RowFilter): Promise<Locator> {
  await expandBackgroundTasksSection(page)
  await expect(backgroundTasksSection(page)).toBeVisible()
  let row = page.locator('[data-testid="bg-task-row"]:visible')
  if (filter.kind)
    row = row.filter({ has: page.locator(`[data-kind="${filter.kind}"]`) })
  // data-status / data-kind are attributes ON the row element itself in some
  // render paths and on children in others; match both.
  if (filter.status) {
    row = row.filter({
      has: page.locator(`[data-status="${filter.status}"], [data-status="${filter.status}"]`),
    }).or(
      page.locator(`[data-testid="bg-task-row"]:visible[data-status="${filter.status}"]`),
    )
  }
  if (filter.titleContains)
    row = row.filter({ hasText: filter.titleContains })
  await expect(row.first()).toBeVisible()
  return row.first()
}

/** The end label the row shows for each final status. */
const END_LABELS: Record<string, string> = {
  completed: 'Completed',
  failed: 'Failed',
  stopped: 'Stopped',
  interrupted: 'Interrupted',
}

/**
 * Poll until the row reaches a final status, then assert its secondary line
 * shows THAT status's end label.
 *
 * The label is derived from the status the poll settled on, never hardcoded: a
 * subagent that legitimately ends Stopped or Failed is still a subagent that
 * ended, and demanding 'Completed' turned those runs into failures that named
 * the wrong thing.
 */
export async function expectRowBecomesFinal(page: Page, row: Locator): Promise<void> {
  let settled: string | null = null
  await expect.poll(async () => {
    const status = await row.getAttribute('data-status')
    settled = FINAL_STATUSES.includes(status as typeof FINAL_STATUSES[number]) ? status : null
    return settled
  }).not.toBeNull()
  const label = END_LABELS[settled ?? '']
  if (label)
    await expect(row.filter({ hasText: label })).toBeVisible()
}

/**
 * Worker-backed: poll `listAgents` until at least one agent with the given
 * `parentAgentId` appears. Asserts `spawnSpanId` is non-empty (the tab/spawn
 * correlation key). Returns the child agent id + spawnSpanId.
 */
export async function waitForChildAgent(
  hubUrl: string,
  token: string,
  workerId: string,
  tabIds: string[],
  parentAgentId: string,
): Promise<{ id: string, spawnSpanId: string }> {
  let child: { id: string, spawnSpanId: string } | null = null
  await expect.poll(async () => {
    const agents = await listAgents(hubUrl, token, workerId, tabIds)
    if (!agents)
      return null
    const found = agents.find(a => a.parentAgentId === parentAgentId)
    child = found ? { id: found.id, spawnSpanId: found.spawnSpanId } : null
    return child
  }).not.toBeNull()
  expect(child!.spawnSpanId).not.toBe('')
  return child!
}

/** Assert the section header and its rows remain visible after tasks finish. */
export async function expectSectionPersists(page: Page): Promise<void> {
  await expect(backgroundTasksSection(page)).toBeVisible()
  await expect(page.locator('[data-testid="bg-task-row"]:visible').first()).toBeVisible()
}

/**
 * Count the session-goal TRANSITIONS the worker persisted, read over the E2EE
 * test channel.
 *
 * Worker-backed rather than read off the screen, for the reason every registry
 * assertion in this file is: the chat is a virtual list, so a row scrolled out
 * of view is not in the DOM at all and a text count would report whatever the
 * viewport happens to hold.
 *
 * Returns null while the channel is re-establishing, so a caller polls.
 */
export async function countGoalTransitions(
  hubUrl: string,
  token: string,
  workerId: string,
  agentId: string,
): Promise<number | null> {
  const channel = await getTestChannel(hubUrl, token)
  try {
    const resp = await channel.callWorker(
      workerId,
      'ListAgentMessages',
      ListAgentMessagesRequestSchema,
      ListAgentMessagesResponseSchema,
      { agentId, limit: 200 },
    )
    return countGoalTransitionsInMessages(resp.messages ?? [])
  }
  catch {
    return null
  }
}

/**
 * Read full agent information through the encrypted test channel.
 * Return null after a failed read so the caller can wait for reconnection.
 */
export async function listAgents(
  hubUrl: string,
  token: string,
  workerId: string,
  tabIds: string[],
): Promise<AgentInfo[] | null> {
  const channel = await getTestChannel(hubUrl, token)
  try {
    const resp = await channel.callWorker(
      workerId,
      'ListAgents',
      ListAgentsRequestSchema,
      ListAgentsResponseSchema,
      { tabIds },
    )
    return resp.agents
  }
  catch (error) {
    console.warn('Could not list agents:', error instanceof Error ? error.message : typeof error)
    return null
  }
}
