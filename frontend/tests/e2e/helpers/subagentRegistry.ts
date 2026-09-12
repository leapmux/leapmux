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
 */
import type { Locator, Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, ListAgentsRequestSchema, ListAgentsResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect } from '../fixtures'
import { getTestChannel } from './api'
import { countGoalTransitionsInMessages } from './goalTransitions'

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
  await page.locator('[data-testid="goal-actions-trigger"]:visible').click()
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
 * Best-effort variant of waitForRegistryRow: returns the row locator if a
 * registry row appears, or null if the model did not spawn a subagent. Used by
 * registry-only specs where LLM non-cooperation (the model choosing not to use
 * its task tool) is an expected outcome, not a test bug -- the spec skips its
 * spawn-dependent assertions when null is returned.
 */
export async function tryWaitForRegistryRow(page: Page, kind: 'subagent' | 'shell' = 'subagent'): Promise<Locator | null> {
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
 * Wait for a registry row, or SKIP the spec when the model chose not to spawn.
 *
 * The nine specs that drive a real model all need this same three-line dance, so
 * it lives here once rather than being pasted a tenth time. Takes the spec's own
 * `test` object (each provider suite extends its own fixtures) and returns a
 * non-null row, so the caller needs no `!`.
 */
export async function requireRegistryRow(
  test: { skip: (condition: boolean, description: string) => void },
  page: Page,
  kind: 'subagent' | 'shell' = 'subagent',
): Promise<Locator> {
  const row = await tryWaitForRegistryRow(page, kind)
  test.skip(!row, kind === 'shell'
    ? 'model did not run the command'
    : 'model did not spawn a subagent')
  return row!
}

/**
 * Open a subagent's transcript from its sidebar row and return the new tab's id.
 *
 * Counting the agent tabs, clicking, asserting the count grew by one, and
 * reading the id off the newly-rendered tab is the same five statements in every
 * spec that opens a child, and the id must come from the rendered tab strip --
 * the hub's tab list is empty throughout these runs.
 */
export async function openChildTabFromRow(page: Page, row: Locator): Promise<string> {
  const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
  const tabsBefore = await agentTabs.count()
  await row.click()
  await expect(agentTabs).toHaveCount(tabsBefore + 1)
  const childTabId = await agentTabs.nth(tabsBefore).getAttribute('data-tab-id') ?? ''
  expect(childTabId).not.toBe('')
  return childTabId
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
 * Assert the row is registry-only: no child-agent-id, not a button, and
 * clicking it does not change the agent-tab count.
 */
export async function expectRowNotClickable(page: Page, row: Locator): Promise<void> {
  const childId = await row.getAttribute('data-child-agent-id')
  expect(childId ?? '').toBe('')
  // A registry-only row is not a <button>, so clicking it must not open a tab.
  // Best-effort click (the row may not satisfy Playwright's actionability checks,
  // which is itself evidence it is not interactive); the global timeout applies.
  const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
  await row.click().catch(() => {})
  await page.waitForTimeout(500)
  const tabsAfter = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
  expect(tabsAfter).toBe(tabsBefore)
}

/**
 * The shared tail of every REGISTRY-ONLY provider spec (172, 173, 175-177):
 * the row reaches a final status, the section survives it, the row is not
 * clickable, and the provider linked no child transcript.
 *
 * One helper rather than five copies, so a change to what "registry-only"
 * guarantees -- expectNoChildAgents was rewritten once already, after the
 * original version turned out to assert nothing -- lands in one place instead
 * of being pasted a sixth time by the next provider spec.
 *
 * Every caller asserts the final status strictly. `expectRowBecomesFinal` is an
 * `expect.poll` under the global timeout, so it IS the wait a still-settling row
 * needs -- demoting it to a warning for the specs that do not call
 * waitForAgentIdle first meant a row that never finished passed five of them.
 */
export async function expectRegistryOnlySubagentEnds(
  page: Page,
  row: Locator,
): Promise<void> {
  await expectRowBecomesFinal(page, row)
  await expectSectionPersists(page)
  await expectRowNotClickable(page, row)
  await expectNoChildAgents(page)
}

/**
 * Assert this provider linked NO child transcript to any of its subagent rows.
 *
 * Read off the registry rows, NOT from `listAgents`. `listAgents` resolves
 * strictly by the ids it is handed, and a registry-only provider's child --
 * the thing whose absence is under test -- never has a tab, so no id list
 * assembled from open tabs can contain one. Handing it the open tab ids
 * therefore asked the worker about the ROOT and filtered its answer for
 * children, which is 0 whether or not the provider misbehaved: the assertion
 * could not fail. Reading the rows is not a weaker check, it is the only one
 * available -- `data-child-agent-id` is the worker's own linkage, broadcast
 * from the background-task registry rather than derived from CRDT tab state.
 *
 * Requires at least one row, so an empty registry (nothing rendered yet, or a
 * selector that stopped matching) fails loudly instead of passing vacuously
 * for a second time.
 */
export async function expectNoChildAgents(page: Page): Promise<void> {
  const rows = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]')
  await expect.poll(async () => rows.count()).toBeGreaterThan(0)

  const childIds = await rows.evaluateAll(els =>
    els.map(el => el.getAttribute('data-child-agent-id') ?? ''),
  )
  expect(childIds.filter(id => id !== '')).toEqual([])
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
