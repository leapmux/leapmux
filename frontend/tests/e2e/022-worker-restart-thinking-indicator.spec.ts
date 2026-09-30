import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, loginViaToken, openWorkspace, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, waitForAgentIdle } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('worker restart thinking indicator', () => {
  test('should hide thinking indicator when worker goes offline during agent turn', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Thinking Indicator Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Start a turn and hold it open. The test stops the worker while the
      // agent waits, so the answer must not arrive first: against the mock
      // endpoint an unheld turn finishes in milliseconds, and the indicator
      // would be gone before the assertion below ran.
      await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
      await editor.click()
      await page.keyboard.type(modelScript.prompt('Write a very long essay about the history of computing. Make it extremely detailed.'))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await modelScript.waitForSteps(1)

      // Wait for the thinking indicator while the agent works.
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      await expect(thinkingIndicator).toBeVisible()

      // Stop the worker while agent is working
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)

      // Thinking indicator should disappear (agent status becomes INACTIVE)
      await expect(thinkingIndicator).not.toBeVisible()

      // Interrupt button should also disappear
      const interruptButton = page.locator('[data-testid="interrupt-button"]')
      await expect(interruptButton).not.toBeVisible()
    }
    finally {
      await restartWorker(separateHubWorker).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should resume agent after worker restart and new message', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Agent Resume Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for a response
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await editor.click()
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')

      // Wait for the assistant's response
      await expectAssistantAnswer(page)
      await waitForAgentIdle(page)

      // Stop the worker
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)

      // Thinking indicator should not be visible
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      await expect(thinkingIndicator).not.toBeVisible()

      // Restart the worker
      await restartWorker(separateHubWorker)

      // The Worker restarts the agent process. Its idle turn shows no thinking indicator.
      await expect(thinkingIndicator).not.toBeVisible()

      // Install a MutationObserver BEFORE sending the message so we can
      // detect even a brief flash of the thinking indicator.
      await page.evaluate(() => {
        Reflect.set(window, '__thinkingIndicatorSeen', false)
        const observer = new MutationObserver(() => {
          if (document.querySelector('[data-testid="thinking-indicator"]')) {
            Reflect.set(window, '__thinkingIndicatorSeen', true)
            observer.disconnect()
          }
        })
        Reflect.set(window, '__thinkingIndicatorObserver', observer)
        observer.observe(document.body, { childList: true, subtree: true })
        // Also check immediately in case it's already visible.
        if (document.querySelector('[data-testid="thinking-indicator"]')) {
          Reflect.set(window, '__thinkingIndicatorSeen', true)
          observer.disconnect()
        }
      })

      let sawThinking = false
      try {
        // A new message reaches the resumed agent. The distinct answer cannot
        // match the first turn's saved bubble.
        await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
        await editor.click()
        await page.keyboard.type(modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
        await page.keyboard.press('Meta+Enter')
        await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
      }
      finally {
        sawThinking = await page.evaluate(() => {
          const observer = Reflect.get(window, '__thinkingIndicatorObserver')
          if (observer instanceof MutationObserver)
            observer.disconnect()
          Reflect.deleteProperty(window, '__thinkingIndicatorObserver')
          const seen = Reflect.get(window, '__thinkingIndicatorSeen') === true
          Reflect.deleteProperty(window, '__thinkingIndicatorSeen')
          return seen
        })
      }

      // Detect even a short indicator before streaming starts.
      expect(sawThinking).toBe(true)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
