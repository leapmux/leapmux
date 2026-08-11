/**
 * Shared helpers for the subagent / background-task E2E specs (170-177).
 *
 * These wrap the common registry assertions so each per-provider spec stays
 * small. All locators are `:visible`-scoped (chat rows + the sidebar render
 * twice) and all worker-state reads go through the E2EE test channel (never
 * optimistic CRDT state). No per-call timeout overrides -- Playwright's global
 * expect timeout (playwright.config.ts) applies.
 */
import type { Locator, Page } from '@playwright/test'
import { ListAgentsRequestSchema, ListAgentsResponseSchema } from '../../../src/generated/leapmux/v1/agent_pb'
import { expect } from '../fixtures'
import { getTestChannel } from './api'

const FINAL_STATUSES = ['completed', 'failed', 'stopped', 'interrupted'] as const

/** Locator for the Background tasks section header (right sidebar). */
export function backgroundTasksSection(page: Page): Locator {
  // `:visible`-scoped like every other locator in this file: the sidebar is
  // mounted twice (the desktop and the mobile tree both render), so the bare
  // test id matches two elements and every strict-mode call on it throws once
  // the section exists.
  return page.locator('[data-testid="section-header-background_tasks"]:visible')
}

/** Expand the section if collapsed (mirrors the Workers-section pattern). */
export async function expandBackgroundTasksSection(page: Page): Promise<void> {
  const section = backgroundTasksSection(page)
  const isOpen = await section.evaluate(el => !el.hasAttribute('data-closed')).catch(() => true)
  if (!isOpen)
    await section.locator('> [role="button"]').click()
}

/**
 * Verify the registry section is absent before a spawn (an empty registry hides
 * the section). Uses a short timeout so it fails fast if a stale row leaked in
 * from a previous test, rather than swallowing the isVisible() result silently.
 * A provider can briefly surface startup activity, so this is a best-effort
 * assertion -- the post-spawn row assertions are the real gate.
 */
export async function expectRegistrySectionAbsent(page: Page): Promise<void> {
  // Wait a beat for the initial registry broadcast to settle, then assert.
  await page.waitForTimeout(1000)
  await expect(backgroundTasksSection(page), 'registry section should be absent before spawn').not.toBeVisible()
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
 * Call the worker's `ListAgents` RPC over the E2EE test channel. Returns null
 * while the channel is re-establishing (caller polls). Reads the full agent
 * info including parentAgentId / spawnSpanId / acceptsMessages.
 */
export async function listAgents(
  hubUrl: string,
  token: string,
  workerId: string,
  tabIds: string[],
): Promise<Array<{ id: string, parentAgentId: string, spawnSpanId: string, acceptsMessages: boolean }> | null> {
  const channel = await getTestChannel(hubUrl, token)
  try {
    const resp = await channel.callWorker(
      workerId,
      'ListAgents',
      ListAgentsRequestSchema,
      ListAgentsResponseSchema,
      { tabIds },
    )
    return (resp.agents ?? []).map(a => ({
      id: a.id,
      parentAgentId: a.parentAgentId,
      spawnSpanId: a.spawnSpanId,
      acceptsMessages: a.acceptsMessages,
    }))
  }
  catch {
    return null
  }
}
