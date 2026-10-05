import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { countOriginalAnswerRows, expectNativeResumeContext, expectResumedConversation, nativeResumeTexts } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, loginViaToken, openWorkspace, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, waitForAgentIdle } from '../helpers/ui'
import { ensureWorkerOnline, restartWorker, stopWorker, processTest as test } from '../process-control-fixtures'

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
    const texts = nativeResumeTexts()
    const priorAnswerMarker = texts.originalAnswer
    const firstPrompt = `${ARITHMETIC_PROMPT} ${texts.originalPrompt}`
    const secondPrompt = `${SECOND_ARITHMETIC_PROMPT} ${texts.resumedPrompt}`
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Resume Test')
    const agentId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    const server = { leapmuxServer: { hubUrl, adminToken, workerId } }
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response
      await editor.click()
      await modelScript.queue({ text: `${ARITHMETIC_ANSWER_TEXT} ${priorAnswerMarker}` })
      await page.keyboard.type(modelScript.prompt(firstPrompt))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      const initial = await modelScript.waitForSteps(1)
      expect(JSON.stringify(initial.requests.find(request => request.stepIndex === 0)?.body)).not.toContain(priorAnswerMarker)

      // Wait for the assistant's response
      await expectAnswerAndTurnEnd(page)
      const originalAnswerRows = await countOriginalAnswerRows(server, agentId, texts)

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
      await modelScript.queue({ text: `${SECOND_ARITHMETIC_ANSWER_TEXT} ${texts.resumedAnswer}` })
      await page.keyboard.type(modelScript.prompt(secondPrompt))
      await page.keyboard.press('Meta+Enter')
      const resumed = await modelScript.waitForSteps(2)
      const nextRequest = resumed.requests.find(request => request.stepIndex === 1)
      if (!nextRequest)
        throw new Error('The resumed prompt reached no native model request.')
      const nextBody = JSON.stringify(nextRequest.body)
      expect(nextBody).toContain(priorAnswerMarker)
      expect(nextBody).toContain(SECOND_ARITHMETIC_PROMPT)
      expectNativeResumeContext(nativeModelConversationTurns(nextRequest), texts)

      // Wait for the answer of the resumed turn. The answer "3333" does not
      // occur in the first answer "6912", so this check cannot match the
      // earlier bubble.
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
      await waitForAgentIdle(page)
      await expectResumedConversation({ page, ...server }, agentId, texts, originalAnswerRows)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })
})
