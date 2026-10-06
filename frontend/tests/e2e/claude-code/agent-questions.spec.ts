import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { AgentActivityState, AgentProvider, ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from '../helpers/api'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { currentIdleReceipt, observeSettledReceipts } from '../helpers/turnEndSound'
import { agentTabs, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, collapseWorkspaceRow, composerEditor, controlBanner, controlButton, expectAssistantAnswer, expectNoControlBanner, focusComposer, loginViaToken, openAgentViaUI, openWorkspace, questionPagination, resumePausedQueue, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, sidebarLeaves, waitForAgentIdle, waitForControlBanner, waitForEditorDraft, waitForWorkspaceReady, workspaceRow, workspaceRowTitle } from '../helpers/ui'

/** Click the displayed question option of the visible banner. Its control and label forward selection to the native input. */
async function clickOption(page: Page, label: string) {
  const option = controlBanner(page).getByTestId(`question-option-${label}`)
  await expect(option).toBeVisible()
  await option.click()
}

// The mock scripts the actual native question tool. The banner must show these exact options.

const COLOR_Q_3: QuestionRequest = {
  question: 'Pick a color',
  header: 'Color',
  options: [
    { label: 'Red', description: 'Red color' },
    { label: 'Blue', description: 'Blue color' },
    { label: 'Green', description: 'Green color' },
  ],
}

const COLOR_Q_2: QuestionRequest = {
  question: 'Pick a color',
  header: 'Color',
  options: [
    { label: 'Red', description: 'Red color' },
    { label: 'Blue', description: 'Blue color' },
  ],
}

const SIZE_Q: QuestionRequest = {
  question: 'Pick a size',
  header: 'Size',
  options: [
    { label: 'Small', description: 'Small size' },
    { label: 'Large', description: 'Large size' },
  ],
}

/** Script a native question and return the preceding request count for its actual answer proof. */
async function askQuestions(
  page: Page,
  script: ModelScript,
  questions: QuestionRequest[],
  options: { gate?: string } = {},
): Promise<number> {
  const before = (await script.status()).requests.length
  await script.fallback({ text: 'You answered the questions.' })
  await script.queue({
    toolCalls: [askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'ask-user', questions)],
    ...(options.gate === undefined ? {} : { gate: options.gate }),
  })
  await sendMessage(page, script.prompt('Use AskUserQuestion and tell me what I answered.'))
  if (options.gate === undefined)
    await script.waitForSteps()
  else
    await script.waitForGate(options.gate)
  return before
}

/** Prove selected answers in the new native call-ID result, then require completion in the browser. */
async function expectQuestionAnswers(page: Page, script: ModelScript, before: number, values: readonly string[]): Promise<void> {
  await waitForAgentIdle(page)
  const status = await script.status()
  const request = status.requests.slice(before + 1).find(record => record.fallback === true)
  if (!request)
    throw new Error('The question answer reached no following native model request.')
  expect(request.protocol).toBe('anthropic-messages')
  const answer = nativeToolResult(request, 'ask-user')
  for (const value of values)
    expect(answer).toContain(value)
  await expectNoControlBanner(page)
}

/** Submit the answers of the visible banner once Submit accepts them. */
async function submitAnswers(page: Page): Promise<void> {
  const submit = controlButton(page, 'submit')
  await expect(submit).toBeEnabled()
  await submit.click()
}

claudeTest.describe('Control Request - AskUserQuestion', () => {
  claudeTest('single question - select an option and submit', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Send a message that triggers AskUserQuestion
    const before = await askQuestions(page, modelScript, [COLOR_Q_3])

    // Wait for the control banner
    const banner = await waitForControlBanner(page)

    // Verify question text and options (scoped to banner to avoid matching chat messages)
    await expect(banner.getByText('Pick a color')).toBeVisible()
    await expect(banner.getByTestId('question-option-Red')).toBeVisible()
    await expect(banner.getByTestId('question-option-Blue')).toBeVisible()
    await expect(banner.getByTestId('question-option-Green')).toBeVisible()

    // Click "Blue" option
    await clickOption(page, 'Blue')

    // Verify Stop and Submit buttons are visible
    await expect(controlButton(page, 'stop')).toBeVisible()
    await expect(controlButton(page, 'submit')).toBeVisible()

    await submitAnswers(page)

    await waitForAgentIdle(page)
    const status = await modelScript.status()
    const answerRequest = status.requests.slice(before).find(request => request.fallback === true)
    expect(answerRequest?.protocol).toBe('anthropic-messages')
    const answer = nativeToolResult(answerRequest, 'ask-user')
    expect(answer).toContain('Blue')
    expect(answer).not.toContain('Red color')
    expect(answer).not.toContain('Green color')
  })

  claudeTest('multi-question - pagination with option selection', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Send a message with 2 questions
    const before = await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

    const banner = await waitForControlBanner(page)

    // Verify only question 1 is shown (scoped to banner)
    await expect(banner.getByText('Pick a color')).toBeVisible()
    await expect(banner.getByText('Pick a size')).not.toBeVisible()

    // Verify pagination shows 2 page items
    const pagination = questionPagination(page)
    await expect(pagination).toBeVisible()
    const pageButtons = pagination.locator('button')
    await expect(pageButtons).toHaveCount(2)

    // Answer question 1 by clicking "Red" -- should auto-advance to page 2
    await clickOption(page, 'Red')

    // Verify question 2 is now shown (scoped to banner)
    await expect(banner.getByText('Pick a size')).toBeVisible()
    await expect(banner.getByText('Pick a color')).not.toBeVisible()

    // Answer question 2 by clicking "Large"
    await clickOption(page, 'Large')

    await submitAnswers(page)

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Large'])
  })

  claudeTest('multi-question - option click auto-advances to next page', async ({ page, authenticatedWorkspace, modelScript }) => {
    const before = await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

    const banner = await waitForControlBanner(page)

    // Verify page 1 shown (scoped to banner)
    await expect(banner.getByText('Pick a color')).toBeVisible()

    // Click "Red" -- should auto-advance
    await clickOption(page, 'Red')

    // Verify auto-advanced to page 2 (scoped to banner)
    await expect(banner.getByText('Pick a size')).toBeVisible()

    // Click "Large" on page 2 -- should stay on page 2 (last page)
    await clickOption(page, 'Large')
    await expect(banner.getByText('Pick a size')).toBeVisible()

    await submitAnswers(page)

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Large'])
  })

  claudeTest('YOLO button fills unanswered questions', async ({ page, authenticatedWorkspace, modelScript }) => {
    const before = await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

    await waitForControlBanner(page)

    // Answer only question 1
    await clickOption(page, 'Red')

    // YOLO stays available while the second question is unanswered.
    const yolo = controlButton(page, 'yolo')
    await expect(yolo).toBeVisible()

    // Click YOLO
    await yolo.click()

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Go with the recommended option.'])
  })

  claudeTest('Stop button rejects the request', async ({ page, authenticatedWorkspace, modelScript }) => {
    await askQuestions(page, modelScript, [COLOR_Q_2])

    await waitForControlBanner(page)

    // Click Stop
    await controlButton(page, 'stop').click()

    // Verify control banner disappears
    await expectNoControlBanner(page)
  })

  claudeTest('multi-question control request stays on the correct agent tab', async ({ page, authenticatedWorkspace, modelScript }) => {
    const tabs = agentTabs(page)
    await expect(tabs).toHaveCount(1)

    // Open a second agent tab; the new tab becomes active.
    await openAgentViaUI(page)
    await expect(tabs).toHaveCount(2)

    const firstAgentTab = tabs.first()
    const secondAgentTab = tabs.nth(1)

    // Trigger AskUserQuestion only on the second agent.
    await secondAgentTab.click()
    const before = await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

    const secondBanner = await waitForControlBanner(page)
    await expect(secondBanner.getByText('Pick a color')).toBeVisible()

    // Switch to the first agent and verify the control request did not leak there.
    // The second agent's request stays pending, so the check counts visible banners only.
    await firstAgentTab.click()
    await expect(firstAgentTab).toHaveAttribute('aria-selected', 'true')
    await expect(controlBanner(page)).toHaveCount(0)

    // Switch back to the second agent and complete the request there.
    await secondAgentTab.click()
    await expect(secondAgentTab).toHaveAttribute('aria-selected', 'true')
    const banner = await waitForControlBanner(page)
    await expect(banner.getByText('Pick a color')).toBeVisible()

    await clickOption(page, 'Red')
    await expect(banner.getByText('Pick a size')).toBeVisible()
    await clickOption(page, 'Large')

    await submitAnswers(page)

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Large'])
  })

  claudeTest('control request on a background agent tab badges it', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    const tabs = agentTabs(page)
    await openAgentViaUI(page)
    await expect(tabs).toHaveCount(2)

    // Hold the actual response until agent 2 is selected. The new question must notify the hidden agent's tab.
    const gate = 'claude-question-background-tab'
    await tabs.first().click()
    await askQuestions(page, modelScript, [COLOR_Q_2], { gate })
    try {
      await tabs.nth(1).click()
      await expect(tabs.nth(1)).toHaveAttribute('aria-selected', 'true')
      await modelScript.releaseGate(gate)

      await expect(tabs.first().locator('[data-testid="tab-notification"]')).toBeVisible()
      // The first agent's request is pending, so the check counts visible banners only.
      await expect(controlBanner(page)).toHaveCount(0)

      await tabs.first().click()
      await expect(tabs.first().locator('[data-testid="tab-notification"]')).not.toBeVisible()
      await waitForControlBanner(page)
    }
    finally {
      await modelScript.releaseGateIfHeld(gate)
    }
  })

  claudeTest('control request on a background workspace badges its tab when returned to', async ({ page, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Active')
    let ws2 = ''
    const gate = 'claude-question-background-workspace'
    await withCleanup(async () => {
      ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Background')
      await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
      await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)
      await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)
      await withCleanup(async () => {
        await loginViaToken(page, adminToken)
        await openWorkspace(page, ws2)
        await waitForWorkspaceReady(page)

        const tabs = agentTabs(page)
        await expect(tabs).toHaveCount(2)
        // Hold the response until the other workspace becomes active. Agent 1 must retain its badge after return.
        await tabs.first().click()
        await askQuestions(page, modelScript, [COLOR_Q_2], { gate })
        await tabs.nth(1).click()
        await workspaceRowTitle(page, ws1).click()
        await waitForWorkspaceReady(page)
        await modelScript.releaseGate(gate)

        // The sidebar must show the notification while the other workspace's tab strip stays off screen.
        const sidebarMarker = '[data-testid="sidebar-tab-notification"]'
        await expect(sidebarLeaves(page, ws2).locator(sidebarMarker)).toHaveCount(1)

        // A folded workspace shows the notification on its own row.
        // Its leaves remain mounted and an expanded child can retain visibility inside the clipped grid.
        // Use data-expanded to prove the workspace's actual fold state.
        await collapseWorkspaceRow(page, ws2)
        await expect(workspaceRow(page, ws2).locator(sidebarMarker)).toBeVisible()

        // Return with agent 2 selected. Require agent 1's notification after its background question arrives.
        await expect(async () => {
          await workspaceRowTitle(page, ws2).click()
          await waitForWorkspaceReady(page)
          expect(await tabs.first().locator('[data-testid="tab-notification"]').count()).toBe(1)
        }).toPass()

        // Activating the workspace expands it. The notification returns from its workspace row to its own leaf.
        await expect(workspaceRow(page, ws2)).toHaveAttribute('data-expanded', 'true')
        await expect(workspaceRow(page, ws2).locator(sidebarMarker)).toHaveCount(0)
        await expect(sidebarLeaves(page, ws2).locator(sidebarMarker)).toHaveCount(1)
      }, () => modelScript.releaseGateIfHeld(gate).then(() => {}))
    }, () => finishCleanup([
      deleteWorkspaceViaAPI(hubUrl, adminToken, ws1),
      ws2 ? deleteWorkspaceViaAPI(hubUrl, adminToken, ws2) : Promise.resolve(),
    ]))
  })
})

const COLOR_QUESTION: QuestionRequest = {
  question: 'Pick a color',
  header: 'Color',
  options: [
    { label: 'Red', description: 'Red color' },
    { label: 'Blue', description: 'Blue color' },
    { label: 'Green', description: 'Green color' },
  ],
}

/** Script one `AskUserQuestion` call and send the turn that makes it. */
async function askColor(page: Parameters<typeof sendMessage>[0], script: ModelScript): Promise<void> {
  // What the test does with the banner decides how many turns follow.
  await script.fallback({ text: 'You answered the question.' })
  await script.queue({ toolCalls: [askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'ask-color', [COLOR_QUESTION])] })
  await sendMessage(page, script.prompt('Use AskUserQuestion and tell me what I answered.'))
  await script.waitForSteps()
}

claudeTest.describe('Control Request Draft Persistence', () => {
  claudeTest('AskUserQuestion custom text draft survives page reload', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Trigger AskUserQuestion.
    await askColor(page, modelScript)

    // Wait for the control banner.
    await waitForControlBanner(page)

    // Type custom text in the editor.
    await focusComposer(page)
    await page.keyboard.type('my custom color answer', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'my custom color answer')

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear.
    await waitForControlBanner(page)

    // Verify the editor still contains the custom text.
    await expect(composerEditor(page)).toContainText('my custom color answer')
  })

  claudeTest('control request draft is isolated from conversation draft', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Type a conversation draft first.
    await focusComposer(page)
    await page.keyboard.type('conversation draft text', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'conversation draft text')

    // Clear the editor and send a message to trigger AskUserQuestion.
    await page.keyboard.press('Meta+a')
    await page.keyboard.press('Backspace')
    await askColor(page, modelScript)

    // Wait for the control banner.
    await waitForControlBanner(page)

    // Require an empty editor. The control request uses a separate draft key from the conversation.
    await expect(composerEditor(page)).toHaveText('')

    // Type control request draft text.
    await focusComposer(page)
    await page.keyboard.type('control request draft text', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'control request draft text')

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear.
    await waitForControlBanner(page)

    // Verify editor contains the control request draft (not the conversation draft).
    const restoredEditor = composerEditor(page)
    await expect(restoredEditor).toContainText('control request draft text')
    await expect(restoredEditor).not.toContainText('conversation draft text')
  })
})

claudeTest.describe('Agent Settings', () => {
  claudeTest('interrupt via control request', async ({ native }) => {
    const { page, modelScript, leapmuxServer } = native
    await expect(composerEditor(page)).toBeVisible()

    // Send a quick message to ensure the agent is fully started
    const first = await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(first + 1)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    const agent = await currentNativeAgent(native)
    await askQuestions(page, modelScript, [COLOR_Q_2])
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Pick a color')
    // The open question holds the Worker in WAITING_FOR_USER. The move into that
    // state was the turn's settle edge (see agentActivity.store `apply`).
    await retryUntilPass(async () => {
      expect((await nativeAgentById(native, agent.id))?.activityState, 'the Worker holds the agent waiting for the user').toBe(AgentActivityState.WAITING_FOR_USER)
    })
    const after = await observeSettledReceipts(page)
    await banner.getByTestId('control-interrupt').click()
    await retryUntilPass(async () => {
      expect((await nativeAgentById(native, agent.id))?.activityState, 'the Worker reports the interrupted agent as idle').toBe(AgentActivityState.IDLE)
    })
    // The interrupt withdraws the question, so no banner stays on the page, visible or hidden.
    await expectNoControlBanner(page)
    await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
    // The thinking indicator is already absent in WAITING_FOR_USER, and the Worker API
    // reports the state before the browser applies it. The Interrupt button shows only
    // while the browser holds WORKING or WAITING_FOR_USER, so its absence proves that
    // the browser applied the IDLE report. The browser records a receipt in the same
    // task as that state change, so the check below cannot run before it.
    await expect(page.locator('[data-testid="interrupt-button"]:visible')).toHaveCount(0)
    // WAITING_FOR_USER to IDLE is not a settle edge, because the agent was not
    // working. The stop therefore rings no second alert and records no receipt.
    expect(await currentIdleReceipt(page, { agentId: agent.id, after })).toBeUndefined()
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    await retryUntilPass(async () => {
      const queue = await channel.callWorker(leapmuxServer.workerId, 'ListAgentInputQueue', ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, { agentId: agent.id })
      expect(queue.snapshot?.paused, 'the Worker pauses the input queue after the interrupt').toBe(true)
    })
    await resumePausedQueue(page)

    // Verify the agent is still responsive after interrupt by sending another message
    const next = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(next + 1)
    await expectAssistantAnswer(page)
  })
})
