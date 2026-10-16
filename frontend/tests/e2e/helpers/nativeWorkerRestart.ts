import type { Page } from '@playwright/test'
import type { AgentOpenOverrides } from '../agentSettings'
import type { SeparateServerInfo } from '../process-control-fixtures'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderAgent } from './workspace'
import { expect } from '@playwright/test'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ensureWorkerOnline, restartHub, restartWorker, stopHub, stopWorker, waitForWorkerOffline } from '../process-control-fixtures'
import { withCleanup } from './cleanup'
import { sendNativeAnswer } from './nativeConversation'
import { countOriginalAnswerRows, expectNativeResumeContext, expectResumedConversation, nativeResumeTexts } from './nativeResume'
import { currentNativeAgent, nativeAgentById, nativeModelConversationTurns } from './nativeScenario'
import { retryUntilPass } from './retryUntilPass'
import { assistantBubbles, expectAssistantAnswer, loginViaToken, openWorkspace, reopenWorkspace, waitForAgentIdle } from './ui'
import { openProviderAgent, withTestWorkspace } from './workspace'

/** The workspace that `withRestartWorkspace` opens, and its one provider agent. */
export interface RestartWorkspace {
  workspaceId: string
  agentId: string
}

/**
 * Open a private provider workspace and show it before the operation.
 * The provider supplies its working-directory rule and any option overrides.
 * The workspace cleanup runs after success or failure while the Hub remains available.
 */
export async function withRestartWorkspace(
  page: Page,
  server: SeparateServerInfo,
  options: { prefix: string, provider: ProviderAgent, openOptions?: AgentOpenOverrides },
  operation: (workspace: RestartWorkspace) => Promise<void>,
): Promise<void> {
  await ensureWorkerOnline(server)
  await withTestWorkspace(server, options.prefix, async ({ workspaceId }) => {
    const { agentId } = await openProviderAgent(server, workspaceId, options.provider, options.openOptions)
    await loginViaToken(page, server.adminToken)
    await openWorkspace(page, workspaceId)
    await operation({ workspaceId, agentId })
  })
}

/** Verify the same native session and actual model context after a Worker or full server restart. */
export async function exerciseWorkerRestart(
  context: ManagedNativeScenarioContext,
  server: SeparateServerInfo,
  restart: 'worker' | 'hub-and-worker',
): Promise<void> {
  const texts = nativeResumeTexts()
  await sendNativeAnswer(context, texts.originalPrompt, texts.originalAnswer)
  const prior = await currentNativeAgent(context)
  expect(prior.agentProvider).toBe(context.provider)
  expect(prior.agentSessionId).not.toBe('')
  await context.waitForNativeCheckpoint?.(prior.agentSessionId)
  const originalRows = await countOriginalAnswerRows(context, prior.id, texts)
  const originalBubbles = await assistantBubbles(context.page).filter({ hasText: texts.originalAnswer }).count()
  await withCleanup(async () => {
    await stopWorker(server)
    await waitForWorkerOffline(server)
    await waitForWorkerConnection(context.page, false)
    if (restart === 'hub-and-worker') {
      await stopHub(server)
      await restartHub(server)
    }
    await restartWorker(server)
    if (restart === 'hub-and-worker')
      await reopenWorkspace(context.page, context.workspaceId)
    await waitForWorkerConnection(context.page, true)
    await retryUntilPass(async () => {
      const resumed = await nativeAgentById(context, prior.id)
      expect(resumed?.status, 'the Worker restores an active native agent').toBe(AgentStatus.ACTIVE)
      expect(resumed?.agentProvider).toBe(context.provider)
      expect(resumed?.agentSessionId, 'the restart retains the native session').toBe(prior.agentSessionId)
    })
    const request = await sendNativeAnswer(context, texts.resumedPrompt, texts.resumedAnswer)
    expectNativeResumeContext((context.readConversationTurns ?? nativeModelConversationTurns)(request), texts)
    await expectResumedConversation(context, prior.id, texts, originalRows, originalBubbles)
    await context.page.reload()
    await expectResumedConversation(context, prior.id, texts, originalRows, originalBubbles)
  }, async () => {
    if (server.hubProc.exitCode !== null || server.hubProc.signalCode !== null)
      await restartHub(server)
    await ensureWorkerOnline(server)
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
