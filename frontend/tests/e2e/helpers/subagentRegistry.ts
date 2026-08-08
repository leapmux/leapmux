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

const TERMINAL_STATUSES = ['completed', 'failed', 'stopped', 'interrupted'] as const

/** Locator for the Background tasks section header (right sidebar). */
export function backgroundTasksSection(page: Page): Locator {
  return page.getByTestId('section-header-background_tasks')
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

/**
 * Poll until the row is in a terminal status and its secondary line shows one
 * of the end labels ('Completed' | 'Failed' | 'Stopped' | 'Interrupted').
 */
export async function expectRowBecomesTerminal(page: Page, row: Locator, label?: string): Promise<void> {
  await expect.poll(async () => {
    const status = await row.getAttribute('data-status')
    return TERMINAL_STATUSES.includes(status as typeof TERMINAL_STATUSES[number])
      ? status
      : null
  }).not.toBeNull()
  if (label) {
    await expect(row.filter({ hasText: label })).toBeVisible()
  }
}

/**
 * Assert the row is registry-only: no child-agent-id, not a button, and
 * clicking it does not change the agent-tab count.
 */
export async function expectRowNotClickable(page: Page, row: Locator): Promise<void> {
  const childId = await row.getAttribute('data-child-agent-id')
  expect(childId ?? '').toBe('')
  const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
  await row.click({ timeout: 2000 }).catch(() => {})
  await page.waitForTimeout(500)
  const tabsAfter = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
  expect(tabsAfter).toBe(tabsBefore)
}

/**
 * Worker-backed: poll `listAgents` until it settles, then assert NO returned
 * agent has `parentAgentId` set (registry-only providers must not create child
 * rows). Returns the agents array on success.
 */
export async function expectNoChildAgents(
  hubUrl: string,
  token: string,
  workerId: string,
  tabIds: string[],
): Promise<void> {
  await expect.poll(async () => {
    const agents = await listAgents(hubUrl, token, workerId, tabIds)
    if (!agents)
      return null
    return agents.filter((a: { parentAgentId: string }) => a.parentAgentId !== '').length
  }).toBe(0)
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

/**
 * Get all agent tab IDs for a workspace (hub ListTabs + filter AGENT). Needed
 * to seed ListAgents (which takes tabIds).
 */
export async function agentTabIdsForWorkspace(
  hubUrl: string,
  token: string,
  workspaceId: string,
): Promise<string[]> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/ListTabs`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'cookie': `session=${token}` },
    body: JSON.stringify({ workspaceIds: [workspaceId] }),
  })
  if (!res.ok)
    return []
  const data = await res.json() as { tabs?: Array<{ tabType: string, tabId: string }> }
  return (data.tabs ?? []).filter(t => t.tabType === 'TAB_TYPE_AGENT').map(t => t.tabId)
}
