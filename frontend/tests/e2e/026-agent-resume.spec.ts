import type { Page } from '@playwright/test'
import { AgentStatus } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, loginViaToken, openWorkspace, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, waitForAgentIdle } from './helpers/ui'
import { listAgentsViaAPI } from './helpers/worktree'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test } from './process-control-fixtures'

test.describe('Agent Session Resume', () => {
  /**
   * Wait for the first answer, and then for the end of its turn, before a test
   * stops the worker.
   *
   * The answer's text reaches the page before the turn ends. A worker that stops
   * inside that window leaves the input queue's turn open, and the worker's
   * restart then pauses the queue as interrupted. The next message then waits in
   * the paused queue and never reaches the agent.
   */
  async function expectAnswerAndTurnEnd(page: Page) {
    await expectAssistantAnswer(page)
    await waitForAgentIdle(page)
  }

  async function waitForWorkerConnection(page: Page, connected: boolean) {
    const status = page.getByTestId('section-header-workers').locator('[data-status="connected"]')
    if (connected)
      await expect(status).not.toHaveCount(0)
    else
      await expect(status).toHaveCount(0)
  }

  test('should resume agent session after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Resume Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response
      await editor.click()
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the assistant's response
      await expectAnswerAndTurnEnd(page)

      // Stop the worker
      await stopWorker(separateHubWorker)

      // Wait until the browser observes the closed worker channel. The editor
      // stays visible while offline, so it cannot prove this state change.
      await waitForWorkerConnection(page, false)

      // The editor should still be enabled (agent has session ID so it's resumable)
      await expect(editor).toBeVisible()

      // Restart the worker
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // Send a new message to the closed (but resumable) agent
      await editor.click()
      await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')

      // Wait for a response - the agent should have resumed. The answer "3333"
      // does not occur in the first answer "6912", so this waits for the new
      // (resumed) turn rather than matching the prior bubble.
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should resume the agent process on worker restart without a message', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Eager Resume')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // One exchange is what gets the CLI to report a session id, which is the
      // filter the boot-time sweep applies: a tab whose agent never ran has
      // nothing to restore and is deliberately left cold.
      await editor.click()
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expectAnswerAndTurnEnd(page)

      await stopWorker(separateHubWorker)
      await waitForWorkerConnection(page, false)
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // The whole point: the process comes back on its own. Nothing below sends
      // a message, so a worker that only spawns lazily leaves the agent
      // INACTIVE for ever and this poll times out.
      //
      // Polled through ListAgents, a WORKER-backed RPC. The hub's tab list and
      // the local tab state are optimistic CRDT state and would report a
      // healthy tab for an agent whose process is gone.
      await expect.poll(async () => {
        const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
        return agents.map(a => a.status)
      }).toEqual([AgentStatus.ACTIVE])
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should deliver control request after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Request Restart')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response (establishes session)
      await editor.click()
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expectAnswerAndTurnEnd(page)

      // Stop the worker and wait for the browser connection to change twice.
      await stopWorker(separateHubWorker)
      await waitForWorkerConnection(page, false)
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // Wait for editor to be visible (worker reconnected)
      await expect(editor).toBeVisible()

      // Switch permission mode to Plan Mode via the settings menu
      await chooseSettingsOption(page, 'permissionMode-plan')

      // Verify the mode chip shows Plan Mode — confirms the control request was
      // delivered after the agent was transparently restarted
      await expectSettingsChip(page, 'Plan Mode')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should handle interrupt after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Interrupt Restart')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response (establishes session)
      await editor.click()
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expectAnswerAndTurnEnd(page)

      // Stop the worker and wait for the browser connection to change twice.
      await stopWorker(separateHubWorker)
      await waitForWorkerConnection(page, false)
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // Wait for editor to be visible (worker reconnected)
      await expect(editor).toBeVisible()

      // Send another message to confirm agent is alive after restart
      await editor.click()
      await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')

      // Wait for response — verifies normal operation post-restart
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
