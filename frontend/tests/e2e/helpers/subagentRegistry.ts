/**
 * These helpers drive the background-task registry in end-to-end (E2E) tests.
 * ./goalsAndTodos.ts drives the Goals & To-dos section.
 * Scope locators for present chat rows to :visible because ChatView can premeasure a hidden copy.
 * Select the first visible sidebar mount for section headers because only that mount receives Worker metadata.
 * Keep zero-count registry locators unscoped so a collapsed section cannot hide a forbidden row.
 * Read Worker state through the encrypted test channel, independently of the browser's optimistic tab state.
 *
 * playwright.config.ts sets the shared expect timeout.
 * ./subagentRegistry.test.ts checks the locator rules and the held-child answer.
 * exerciseChildInterrupt uses this registry to open and stop a native child.
 */
import type { Locator, Page } from '@playwright/test'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelMatcher, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeSidebarContext } from './nativeSidebarSnapshot'
import { expect } from '@playwright/test'
import { cleanupOnFailure } from './cleanup'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
import { spawnSubagentToolCall } from './providerToolCalls'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  assistantBubbles,
  expandSidebarSection,
  expectAssistantAnswer,
  sendMessage,
  tabById,
  waitForAgentIdle,
} from './ui'

const FINAL_STATUSES = ['completed', 'failed', 'stopped', 'interrupted'] as const

/** Locator for the Background tasks section header (right sidebar). */
export function backgroundTasksSection(page: Page): Locator {
  // Both sidebar mounts can be visible. Select the first visible mount that receives Worker metadata.
  return page.locator('[data-testid="section-header-background_tasks"]:visible').first()
}

/** Expand the Background tasks section if it is collapsed. */
export async function expandBackgroundTasksSection(page: Page): Promise<void> {
  await expandSidebarSection(backgroundTasksSection(page))
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
    page.locator('[data-testid="bg-task-row"]'),
    'the registry should hold no rows before a spawn',
  ).toHaveCount(0)
  await expect(
    page.locator('[data-testid="bg-task-load-failed"]'),
    'the worker should be able to answer for the registry',
  ).toHaveCount(0)
}

export interface RowFilter {
  kind?: 'subagent' | 'shell' | 'workflow'
  status?: string
  titleContains?: string
}

/**
 * Wait for the requested registry row and return it.
 * The thinking indicator stays visible while background tasks run.
 * A wait for agent idle would therefore block on the child that this helper needs to inspect.
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
 * Return the registry row when it appears, or null when the wait fails.
 * requireRegistryRow converts that null into a clear assertion failure.
 * Every caller scripts the native spawn, so no caller may continue without the row.
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
 * Wait for a registry row and fail the spec if none appears.
 * Every caller scripts the spawn against the mock.
 * A missing row therefore indicates a product or script defect.
 * A skip would conceal that defect.
 * The asserted non-null return lets the caller use the row directly.
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
 * Select the exact native child transcript from its sidebar row.
 * A new or revived child adds one tab. An existing child keeps the current tab set.
 * Both paths require the exact child tab to be visible and selected.
 * Read rendered IDs instead of the Hub's optimistic list.
 * A child can appear beside its parent, so its position does not identify it.
 */
export async function openChildTabFromRow(page: Page, row: Locator): Promise<string> {
  const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]:visible')
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
  const childTab = page.locator(`[data-testid="tab"][data-tab-type="agent"][data-tab-id="${childId}"]:visible`)
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

/** What one provider needs to open the tab of a subagent that keeps working. */
export interface HeldChildCase {
  provider: AgentProvider
  /** Register provider-owned report handling after the child starts and before its release. */
  beforeRelease?: () => Promise<void>
  /** The provider's display title when it differs from the task description. */
  rowTitle?: string
  /**
   * Match only the child's model turn through {@link HELD_CHILD_TASK}.
   * The rule holds each matched request.
   * Test rules precede housekeeping rules.
   * A broad matcher could hold the child's title request also and report two matches for one child.
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
export function heldChildAnswer(test: HeldChildCase): MockModelStep {
  return { ...(test.heldAnswer ?? { text: 'One, two, three.' }), gate: HELD_CHILD_GATE }
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
  /** Release the model response without waiting on browser state. */
  release: () => Promise<boolean>
  /** Release the held native child and confirm the parent returns to idle. */
  finish: () => Promise<void>
}

/**
 * Spawn a subagent whose model turn stays open, and open its tab.
 *
 * The mock holds the child's answer at an explicit gate. When this returns,
 * the child's model request is open, its row is running, and its tab is active.
 */
export async function openHeldChildTab(page: Page, modelScript: ModelScript, test: HeldChildCase): Promise<HeldChild> {
  // The step count after the root's turns. The queue below sets it before the prompt is sent.
  let target = 0
  const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]:visible')
  await expect(agentTabs).toHaveCount(1)
  const rootTabId = await agentTabs.getAttribute('data-tab-id') ?? ''
  expect(rootTabId, 'the root tab has an ID').not.toBe('')
  let sent = false
  const release = () => modelScript.releaseGateIfHeld(HELD_CHILD_GATE)
  const finish = async () => {
    await release()
    if (!sent)
      return
    await modelScript.waitForSteps(target)
    await tabById(page, rootTabId).click()
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
        toolCalls: [spawnSubagentToolCall(test.provider, 'spawn-held-child', {
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
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    const childTabId = await openChildTabFromRow(page, row)
    return { row, childTabId, rootTabId, heldTurns, release, finish }
  }, finish)
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
export async function exerciseChildInterrupt(page: Page, modelScript: ModelScript, test: ChildInterruptCase): Promise<void> {
  const finalStatus = test.finalStatus ?? 'interrupted'
  const child = await openHeldChildTab(page, modelScript, { ...test, rootTurnsAfterSpawn: [{ text: ROOT_AFTER_CHILD_STOP }] })

  const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
  await expect(interrupt).toBeVisible()
  await interrupt.click()

  // The held answer never arrives. Registry status and activity report the stop.
  await expect(child.row).toHaveAttribute('data-status', finalStatus)
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  await expect(interrupt).toHaveCount(0)

  // The root reads the spawn's result and answers with its next turn.
  await modelScript.waitForSteps()
  await tabById(page, child.rootTabId).click()
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
  await tabById(page, child.childTabId).click()
  await expect(page.locator('[data-testid="notification-divider"]:visible').filter({ hasText: /^Subagent (?:completed|failed|stopped|interrupted)$/ })).toHaveCount(0)
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  await page.reload()
  await tabById(page, child.childTabId).click()
  await expect(page.locator('[data-testid="notification-divider"]:visible').filter({ hasText: /^Subagent (?:completed|failed|stopped|interrupted)$/ })).toHaveCount(0)
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  await tabById(page, child.rootTabId).click()
}

/**
 * Resolve a visible registry row matching the filter. Returns the row locator.
 * Throws (via expect) if no match is found within the default timeout.
 */
export async function expectRegistryRow(page: Page, filter: RowFilter): Promise<Locator> {
  await expandBackgroundTasksSection(page)
  await expect(backgroundTasksSection(page)).toBeVisible()
  let row = page.locator('[data-testid="bg-task-row"]:visible')
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
  completed: 'Completed',
  failed: 'Failed',
  stopped: 'Stopped',
  interrupted: 'Interrupted',
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
  if (label)
    await expect(row.filter({ hasText: label })).toBeVisible()
}

/** Assert the section header and its rows remain visible after tasks finish. */
export async function expectSectionPersists(page: Page): Promise<void> {
  await expect(backgroundTasksSection(page)).toBeVisible()
  await expect(page.locator('[data-testid="bg-task-row"]:visible').first()).toBeVisible()
}
