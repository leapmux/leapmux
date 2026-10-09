/**
 * These helpers drive the background-task registry in end-to-end (E2E) tests.
 * ./goalsAndTodos.ts drives the Goals & To-dos section.
 * Scope locators for present chat rows to :visible because ChatView can premeasure a hidden copy.
 * Select the first visible sidebar mount for section headers because only that mount receives Worker metadata.
 * Locate a registry row through `backgroundTaskRows`, which scopes it to :visible. Keep a zero-count registry locator
 * unscoped through `backgroundTaskRowsIncludingHidden`, so a collapsed section cannot hide a forbidden row.
 * Read Worker state through the encrypted test channel, independently of the browser's optimistic tab state.
 *
 * playwright.config.ts sets the shared expect timeout.
 * ./subagentRegistry.test.ts checks the locator rules and the held-child answer.
 * exerciseChildInterrupt uses this registry to open and stop a native child.
 */
import type { Locator, Page } from '@playwright/test'
import type { MockModelMatcher, MockModelStep } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeSidebarContext } from './nativeSidebarSnapshot'
import type { RunningNativeChild } from './runningChildProof'
import { expect } from '@playwright/test'
import { BACKGROUND_TASK_STATUS_TOKEN } from '~/generated/contracts/worker-vocab'
import { cleanupOnFailure, withCleanup } from './cleanup'
import { cssAttributeValue } from './cssAttribute'
import { selectedAgentTabId } from './nativeScenario'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
import { spawnSubagentToolCall } from './providerToolCalls'
import { retryUntilPass } from './retryUntilPass'
import { AGENT_TAB_SELECTOR, tabIdSelector } from './tabSelectors'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, expandSidebarSection, expectAssistantAnswer, interruptButton, sendMessage, tabById, waitForAgentIdle } from './ui'

const FINAL_STATUSES = [
  BACKGROUND_TASK_STATUS_TOKEN.Succeeded,
  BACKGROUND_TASK_STATUS_TOKEN.Failed,
  BACKGROUND_TASK_STATUS_TOKEN.Stopped,
  BACKGROUND_TASK_STATUS_TOKEN.Interrupted,
  BACKGROUND_TASK_STATUS_TOKEN.EndedWithUnknownOutcome,
] as const

/** Locator for the Background tasks section header (right sidebar). */
export function backgroundTasksSection(page: Page): Locator {
  // Both sidebar mounts can be visible. Select the first visible mount that receives Worker metadata.
  return page.locator('[data-testid="section-header-background_tasks"]:visible').first()
}

/** Expand the Background tasks section if it is collapsed. */
export async function expandBackgroundTasksSection(page: Page): Promise<void> {
  await expandSidebarSection(backgroundTasksSection(page))
}

/** The kind of a background task row, as its `data-kind` attribute states it. */
export type BackgroundTaskRowKind = 'subagent' | 'shell' | 'workflow'

/** The attributes that select background task rows. An absent field selects each value of its attribute. */
export interface BackgroundTaskRowSelection {
  kind?: BackgroundTaskRowKind
  /** The `data-task-id` of the row. */
  taskId?: string
  /** The `data-child-agent-id` of the row of a subagent. */
  childAgentId?: string
}

/**
 * The CSS selector of each background task row that `selection` selects, visible or hidden.
 * The selector escapes each ID, because the Worker or a script chooses it.
 */
function backgroundTaskRowSelector(selection: BackgroundTaskRowSelection): string {
  const kind = selection.kind === undefined ? '' : `[data-kind="${selection.kind}"]`
  const taskId = selection.taskId === undefined ? '' : `[data-task-id="${cssAttributeValue(selection.taskId)}"]`
  const childAgentId = selection.childAgentId === undefined ? '' : `[data-child-agent-id="${cssAttributeValue(selection.childAgentId)}"]`
  return `[data-testid="bg-task-row"]${kind}${taskId}${childAgentId}`
}

/**
 * Every visible background task row that `selection` selects.
 * The app mounts the sidebar twice, and both mounts can be visible, so a check of one row takes `.first()`.
 */
export function backgroundTaskRows(page: Page, selection: BackgroundTaskRowSelection = {}): Locator {
  return page.locator(`${backgroundTaskRowSelector(selection)}:visible`)
}

/**
 * Every background task row that `selection` selects, the hidden rows included.
 * Use it for a check that no such row exists: a row in a collapsed section, or in the hidden sidebar mount, must fail
 * that check, and a visible scope would hide the row.
 */
export function backgroundTaskRowsIncludingHidden(page: Page, selection: BackgroundTaskRowSelection = {}): Locator {
  return page.locator(backgroundTaskRowSelector(selection))
}

/**
 * Require a loaded, empty Worker registry before a spawn.
 * An empty DOM cannot prove that the registry has no rows before hydration.
 * The Worker read must succeed and report both loaded snapshot flags.
 *
 * Keep the DOM load-failure check after the Worker check. A browser can still
 * report a failed registry subscription after the direct Worker read succeeds.
 *
 * The zero-count locators stay unscoped. A hidden row in a collapsed section
 * must fail the check. Either sidebar mount can contain that row.
 */
export async function expectNoRegistryRows(page: Page, server: NativeSidebarContext['leapmuxServer']): Promise<void> {
  const snapshot = await readNativeSidebarSnapshot({ page, leapmuxServer: server })
  expect(snapshot.backgroundTasks, 'the Worker registry must hold no task rows before a spawn').toHaveLength(0)
  await expect(
    backgroundTaskRowsIncludingHidden(page),
    'the registry should hold no rows before a spawn',
  ).toHaveCount(0)
  await expect(
    page.locator('[data-testid="bg-task-load-failed"]'),
    'the worker should be able to answer for the registry',
  ).toHaveCount(0)
}

export interface RowFilter {
  kind?: BackgroundTaskRowKind
  status?: string
  titleContains?: string
}

/**
 * Wait for the first visible registry row of `kind` and return it. The spec fails when no row appears.
 *
 * Every caller scripts the spawn or the command against the mock, so a missing row is a product or script defect,
 * and a skip would hide it. The wait expands the section on each attempt, because a section can collapse while it
 * hydrates. An expand click that fails while the section rerenders starts the next attempt, and the final failure
 * states the last error.
 *
 * Do not wait for the agent to become idle before this call: the thinking indicator stays visible while a background
 * task runs, so that wait blocks on the task that the caller wants to inspect.
 */
export async function requireRegistryRow(
  page: Page,
  kind: Exclude<BackgroundTaskRowKind, 'workflow'> = 'subagent',
): Promise<Locator> {
  const row = backgroundTaskRows(page, { kind }).first()
  const missing = kind === 'shell'
    ? 'the scripted command produced no shell row in the registry'
    : 'the scripted spawn produced no subagent row in the registry'
  await retryUntilPass(async () => {
    await expandBackgroundTasksSection(page)
    expect(await row.isVisible(), missing).toBe(true)
  })
  return row
}

/**
 * Select the exact native child transcript from its sidebar row.
 * A new or revived child adds one tab. An existing child keeps the current tab set.
 * Both paths require the exact child tab to be visible and selected.
 * Read rendered IDs instead of the Hub's optimistic list.
 * A child can appear beside its parent, so its position does not identify it.
 */
export async function openChildTabFromRow(page: Page, row: Locator): Promise<string> {
  const agentTabs = page.locator(`${AGENT_TAB_SELECTOR}:visible`)
  const tabIds = async () => agentTabs.evaluateAll(tabs => tabs.map(tab => tab.getAttribute('data-tab-id') ?? ''))
  const beforeIds = await tabIds()
  expect(beforeIds.every(id => id.trim() !== ''), 'every rendered agent tab has an ID').toBe(true)
  const before = new Set(beforeIds)
  expect(before.size, 'rendered agent tab IDs are unique').toBe(beforeIds.length)
  let childId = ''
  await expect.poll(async () => {
    childId = await row.getAttribute('data-child-agent-id') ?? ''
    return childId
  }).toMatch(/\S/)
  const expectedIds = [...new Set([...before, childId])].sort()
  await row.click()
  await expect(agentTabs).toHaveCount(expectedIds.length)
  expect((await tabIds()).sort(), 'the row selects only its exact native child tab').toEqual(expectedIds)
  const childTab = page.locator(`${AGENT_TAB_SELECTOR}${tabIdSelector(childId)}:visible`)
  await expect(childTab).toBeVisible()
  await expect(childTab).toHaveAttribute('aria-selected', 'true')
  return childId
}

/**
 * The task of a subagent whose turn runs until something stops it. A
 * `childTurn` matcher selects on these words.
 */
export const HELD_CHILD_TASK = 'Count slowly to one hundred'

/** The description of that subagent, which its registry row shows. */
export const HELD_CHILD_TITLE = 'Count to one hundred'

/** The report that a held Oh My Pi child yields when the hold releases. */
export const HELD_CHILD_REPORT = 'Counted to one hundred.'

/**
 * The name that a provider derives from {@link HELD_CHILD_TITLE} when its registry
 * row shows a subagent name instead of the description. Pass it as
 * {@link HeldChildCase.rowTitle} for these providers:
 *
 * - Codex shows the `task_name` of its spawn call.
 * - Oh My Pi shows its subagent ID, which is the task `name` of its `task` call.
 * - Codewhale shows the session `name` of its `agent` start call, which has no
 *   description field.
 */
export const HELD_CHILD_NAME = 'count_to_one_hundred'

/** The name of the rule that holds the child's turn open. */
const HELD_CHILD_RULE = 'the child counts until something stops it'
const HELD_CHILD_GATE = 'held-child-answer'

/** The fixtures that a held child needs. The provider decides the spawn call. */
export type HeldChildContext = Pick<ManagedNativeScenarioContext, 'page' | 'modelScript' | 'leapmuxServer' | 'provider'>

/** What one provider needs to open the tab of a subagent that keeps working. */
export interface HeldChildCase {
  /** Register provider-owned report handling after the child starts and before its release. */
  beforeRelease?: () => Promise<void>
  /** The provider's display title when it differs from the task description. */
  rowTitle?: string
  /**
   * Match only the child's model turn through {@link HELD_CHILD_TASK}.
   * The rule holds each matched request, so a broad matcher that takes another request reports two matches for one
   * child. The child's title request repeats the task, but the housekeeping rules have high priority and answer it
   * first.
   */
  childTurn: MockModelMatcher
  /**
   * The child's answer once the hold releases, when text alone does not end its run.
   * The helper applies its hold gate to whatever this carries.
   *
   * Oh My Pi ends a child run with its `yield` tool, and it nudges a child whose
   * turn ends with no tool call (up to three reminders). A text answer sends its
   * child into extra turns that consume the answers scripted for the parent, so
   * an Oh My Pi case passes a `yield` call here.
   */
  heldAnswer?: MockModelStep
  /** The root's turns after the turn that spawns the child, in order. */
  rootTurnsAfterSpawn: MockModelStep[]
}

/**
 * The answer that holds the child's turn open: the provider's own ending under the
 * hold gate, so a caller that supplies `heldAnswer` cannot drop the gate with it.
 */
export function heldChildAnswer(test: Pick<HeldChildCase, 'heldAnswer'>): MockModelStep {
  return { ...(test.heldAnswer ?? { text: 'One, two, three.' }), gate: HELD_CHILD_GATE }
}

/**
 * The subagent that {@link openHeldChildTab} leaves working.
 * `childId` is the ID of the child agent and of its tab. `parentId` is the ID of the root, beside which the child tab
 * opened. `finish` releases the hold, waits for the root turns, selects the root, and requires a final row status.
 */
export interface HeldChild extends RunningNativeChild {
  /** How many requests the rule that holds the child's turn answered. A repeated child request counts again. */
  heldTurns: () => Promise<number>
  /** Release the model response without waiting on browser state. */
  release: () => Promise<boolean>
}

/**
 * Spawn a subagent whose model turn stays open, and open its tab.
 *
 * The mock holds the child's answer at an explicit gate. When this returns,
 * the child's model request is open, its row is running, and its tab is active.
 *
 * The Worker registry must be empty before the spawn. The helper takes the first subagent row as the child, so a row
 * of an earlier state that hydrates late would otherwise become the child.
 *
 * The hold rule is not `once` on purpose. A repeated child request matches it again, so `heldTurns` counts the repeat,
 * and {@link exerciseChildInterrupt} refuses it. A `once` rule would let a fallback answer the repeat and hide it.
 */
export async function openHeldChildTab(context: HeldChildContext, test: HeldChildCase): Promise<HeldChild> {
  const { page, modelScript } = context
  // The step count after the root's turns. The queue below sets it before the prompt is sent.
  let target = 0
  await expect(page.locator(`${AGENT_TAB_SELECTOR}:visible`), 'the workspace holds only the root agent').toHaveCount(1)
  const parentId = await selectedAgentTabId(page)
  await expectNoRegistryRows(page, context.leapmuxServer)
  let sent = false
  const release = () => modelScript.releaseGateIfHeld(HELD_CHILD_GATE)
  // A failed setup can stop before the row exists, so this step requires no row.
  const settle = async () => {
    await release()
    if (!sent)
      return
    await modelScript.waitForSteps(target)
    await tabById(page, parentId).click()
    await waitForAgentIdle(page)
  }
  return cleanupOnFailure(async () => {
    await modelScript.rule({
      name: HELD_CHILD_RULE,
      when: test.childTurn,
      respond: heldChildAnswer(test),
    })
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(context.provider, 'spawn-held-child', {
          description: HELD_CHILD_TITLE,
          prompt: modelScript.prompt(`${HELD_CHILD_TASK}.`),
        })],
      },
      ...test.rootTurnsAfterSpawn,
    )
    target = start + 1 + test.rootTurnsAfterSpawn.length
    await sendMessage(page, modelScript.prompt('Delegate the count to a subagent.'))
    sent = true
    await modelScript.waitForSteps(start + 1)
    const row = await requireRegistryRow(page)
    await expect(row).toContainText(test.rowTitle ?? HELD_CHILD_TITLE)
    await expect(row).toHaveAttribute('data-status', 'running')
    const heldTurns = async () => (await modelScript.status()).ruleMatches[HELD_CHILD_RULE] ?? 0
    await modelScript.waitForGate(HELD_CHILD_GATE)
    expect(await heldTurns()).toBe(1)
    await test.beforeRelease?.()
    const childId = await openChildTabFromRow(page, row)
    const finish = async () => {
      await settle()
      await expectRowBecomesFinal(page, row)
    }
    return { row, childId, parentId, heldTurns, release, finish }
  }, settle)
}

/** What {@link exerciseHeldChildRow} needs beyond the shared held child. */
export interface HeldChildRowCase extends Pick<HeldChildCase, 'rowTitle' | 'heldAnswer'> {
  /** The texts that the running row must hold. The default is the row title: `rowTitle`, or {@link HELD_CHILD_TITLE}. */
  rowTexts?: readonly string[]
}

/**
 * Keep a held child in the Background tasks section of its root through its run: open the child, return to the root,
 * require a running row that holds `rowTexts`, let the child finish, and require a final row that the section keeps.
 * The child finishes even when a check of the running row fails, so that no held request stays open after the test.
 */
export async function exerciseHeldChildRow(context: HeldChildContext, options: HeldChildRowCase = {}): Promise<void> {
  const { page } = context
  const child = await openHeldChildTab(context, {
    ...(options.rowTitle === undefined ? {} : { rowTitle: options.rowTitle }),
    ...(options.heldAnswer === undefined ? {} : { heldAnswer: options.heldAnswer }),
    childTurn: { user: HELD_CHILD_TASK },
    rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }],
  })
  await withCleanup(async () => {
    await tabById(page, child.parentId).click()
    await expect(backgroundTasksSection(page)).toBeVisible()
    await expect(child.row).toHaveAttribute('data-status', 'running')
    for (const text of options.rowTexts ?? [options.rowTitle ?? HELD_CHILD_TITLE])
      await expect(child.row).toContainText(text)
  }, child.finish)
  await expectRowBecomesFinal(page, child.row)
  await expectSectionPersists(page)
}

/** The root's answer after the spawn's result states the stop. */
const ROOT_AFTER_CHILD_STOP = 'The subagent stopped before it finished.'

/** What one provider needs for {@link exerciseChildInterrupt}. */
export type ChildInterruptCase = Omit<HeldChildCase, 'rootTurnsAfterSpawn'> & {
  /**
   * The finalStatus states the native registry outcome and defaults to interrupted.
   * Pass stopped when the provider reports a plain stop instead.
   */
  finalStatus?: 'stopped' | 'interrupted'
}

/**
 * Stop a native child through the Interrupt control on its own tab.
 * Require the stop to end only the child turn.
 * The Worker exposes this control only when AgentInfo.accepts_interrupt is true.
 * The control sends InterruptChild through the Worker.
 * Require each result:
 *
 * - The active child tab offers Interrupt.
 * - The child registry row reports the stop.
 * - The child thinking indicator disappears without a synthetic divider.
 * - The Interrupt control disappears.
 * - The parent continues and accepts its next prompt.
 */
export async function exerciseChildInterrupt(context: HeldChildContext, test: ChildInterruptCase): Promise<void> {
  const { page, modelScript } = context
  const child = await openHeldChildTab(context, { ...test, rootTurnsAfterSpawn: [{ text: ROOT_AFTER_CHILD_STOP }] })
  // A failed stop leaves the child answer held. The release lets the child end, so the stop failure stays the report.
  await cleanupOnFailure(async () => {
    // The held answer never arrives. Registry status and activity report the stop.
    await stopChildWithInterrupt(page, child.row, test.finalStatus ?? 'interrupted')

    // The root reads the spawn's result and answers with its next turn.
    await modelScript.waitForSteps()
    await tabById(page, child.parentId).click()
    await expect(assistantBubbles(page).filter({ hasText: ROOT_AFTER_CHILD_STOP })).toBeVisible()
    // Require the visible parent indicator to disappear.
    // Each tab mounts its own chat, and the hidden child chat can retain an indicator.
    // An unscoped indicator locator can match both chats and fail strict mode.
    await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
    // The stop paused the child's input queue, not the root's, so the next prompt
    // reaches the root at once.
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await expectAssistantAnswer(page)
    // Nothing asked for the child's turn again after the stop.
    expect(await child.heldTurns()).toBe(1)
    // The stop adds no divider and leaves no indicator in the child transcript, also after a reload.
    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await tabById(page, child.childId).click()
      await expect(subagentEndDivider(page)).toHaveCount(0)
      await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
    }
    await tabById(page, child.parentId).click()
  }, async () => {
    await child.release()
  })
}

/** Locate the visible divider that ends a subagent transcript with a final status. */
export function subagentEndDivider(page: Page): Locator {
  return page.locator('[data-testid="notification-divider"]:visible').filter({ hasText: /^Subagent (?:completed|failed|stopped|interrupted)$/ })
}

/**
 * Stop the child of the selected tab through its Interrupt control, and require the stop:
 *
 * - The tab offers Interrupt before the click.
 * - The registry row of the child reports `finalStatus`.
 * - The tab shows no thinking indicator. Each tab mounts its own chat, so the locator reads the visible chat only.
 * - The Interrupt control leaves the tab.
 */
export async function stopChildWithInterrupt(page: Page, row: Locator, finalStatus: 'stopped' | 'interrupted' | 'paused'): Promise<void> {
  const interrupt = interruptButton(page)
  await expect(interrupt).toBeVisible()
  await interrupt.click()
  await expect(row).toHaveAttribute('data-status', finalStatus)
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  await expect(interrupt).toHaveCount(0)
}

/**
 * Resolve a visible registry row matching the filter. Returns the row locator.
 * Throws (via expect) if no match is found within the default timeout.
 */
export async function expectRegistryRow(page: Page, filter: RowFilter): Promise<Locator> {
  await expandBackgroundTasksSection(page)
  await expect(backgroundTasksSection(page)).toBeVisible()
  let row = backgroundTaskRows(page)
  const withAttribute = (rows: Locator, name: 'data-kind' | 'data-status', value: string): Locator => {
    const match = page.locator(`[${name}="${value}"]:visible`)
    const onRow = rows.and(match)
    // A nested field counts only when the row has no value of its own.
    const missingOnRow = rows.and(page.locator(`[data-testid="bg-task-row"]:visible:not([${name}])`))
    return onRow.or(missingOnRow.filter({ has: match }))
  }
  if (filter.kind)
    row = withAttribute(row, 'data-kind', filter.kind)
  if (filter.status)
    row = withAttribute(row, 'data-status', filter.status)
  if (filter.titleContains)
    row = row.filter({ hasText: filter.titleContains })
  await expect(row.first()).toBeVisible()
  return row.first()
}

/** The end label the row shows for each final status. */
const END_LABELS: Record<string, string> = {
  [BACKGROUND_TASK_STATUS_TOKEN.Succeeded]: 'Succeeded',
  [BACKGROUND_TASK_STATUS_TOKEN.Failed]: 'Failed',
  [BACKGROUND_TASK_STATUS_TOKEN.Stopped]: 'Stopped',
  [BACKGROUND_TASK_STATUS_TOKEN.Interrupted]: 'Interrupted',
  [BACKGROUND_TASK_STATUS_TOKEN.EndedWithUnknownOutcome]: 'Ended with unknown outcome',
}

/**
 * Wait for a final registry status, then require the matching status label.
 * Derive the label from the actual final status.
 * A failed or stopped child still reaches a final state and must not require the Completed label.
 */
export async function expectRowBecomesFinal(page: Page, row: Locator): Promise<void> {
  let settled: string | null = null
  await expect.poll(async () => {
    const status = await row.getAttribute('data-status')
    settled = FINAL_STATUSES.includes(status as typeof FINAL_STATUSES[number]) ? status : null
    return settled
  }).not.toBeNull()
  const label = END_LABELS[settled ?? '']
  if (label) {
    await expect(row.locator('[data-testid="bg-task-status-dot"]:visible')).toHaveAttribute('aria-label', label)
    // Select one label per browser query. A title update can add or remove the secondary between separate requests.
    const visibleLabel = row.locator(
      '[data-testid="bg-task-secondary"]:visible, :scope:not(:has([data-testid="bg-task-secondary"]:visible)) [data-testid="bg-task-title"]:visible',
    )
    await expect(visibleLabel).toHaveText(label)
    await expect(row.filter({ hasText: label })).toBeVisible()
  }
}

/** Assert the section header and its rows remain visible after tasks finish. */
export async function expectSectionPersists(page: Page): Promise<void> {
  await expect(backgroundTasksSection(page)).toBeVisible()
  await expect(backgroundTaskRows(page).first()).toBeVisible()
}
