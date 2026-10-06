import type { Page } from '@playwright/test'
import type { SeparateServerInfo } from '../process-control-fixtures'
import { expect } from '@playwright/test'
import { openAgentViaAPI, openPinnedModeAgentViaAPI } from '../helpers/api'
import { expectAssistantAnswer, loginViaToken, openWorkspace, waitForAgentIdle } from '../helpers/ui'
import { withTestWorkspace } from '../helpers/workspace'
import { ensureWorkerOnline } from '../process-control-fixtures'

/** The workspace that `withRestartWorkspace` opens, and its one Claude Code agent. */
export interface RestartWorkspace {
  workspaceId: string
  agentId: string
}

/**
 * Open a workspace with one Claude Code agent on the private Hub and Worker, sign in, and show the workspace. Then
 * run `operation`. `withTestWorkspace` deletes the workspace after it, also when it fails, while the Hub runs.
 *
 * `pinnedMode` opens the agent in the Default permission mode, which a spec that changes the mode starts from.
 */
export async function withRestartWorkspace(
  page: Page,
  server: SeparateServerInfo,
  options: { prefix: string, pinnedMode?: boolean },
  operation: (workspace: RestartWorkspace) => Promise<void>,
): Promise<void> {
  await ensureWorkerOnline(server)
  const { hubUrl, adminToken, workerId } = server
  await withTestWorkspace(server, options.prefix, async ({ workspaceId }) => {
    const agentId = options.pinnedMode
      ? await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
      : await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await operation({ workspaceId, agentId })
  })
}

/**
 * Wait for the first answer, and then for the end of its turn, before a test
 * stops the worker.
 *
 * The answer's text reaches the page before the turn ends. A worker that stops
 * inside that window leaves the input queue's turn open, and the worker's
 * restart then pauses the queue as interrupted. The next message then waits in
 * the paused queue and never reaches the agent.
 */
export async function expectAnswerAndTurnEnd(page: Page): Promise<void> {
  await expectAssistantAnswer(page)
  await waitForAgentIdle(page)
}

/**
 * Wait until the browser shows the Worker as connected, or as not connected.
 * The editor stays visible while the Worker is offline, so it cannot prove this state change.
 *
 * The shell mounts the sidebar twice, for the desktop and the mobile layout, and the locator covers both copies. Only
 * one copy hydrates the Worker metadata, so the count states the status of that copy.
 */
export async function waitForWorkerConnection(page: Page, connected: boolean): Promise<void> {
  const status = page.getByTestId('section-header-workers').locator('[data-status="connected"]')
  if (connected)
    await expect(status).not.toHaveCount(0)
  else
    await expect(status).toHaveCount(0)
}
