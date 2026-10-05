import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { AgentActivityState, AgentProvider, ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { test } from '../fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from '../helpers/api'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { resumeInterruptedQueue } from '../helpers/nativeLifecycle'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { currentIdleReceipt, observeSettledReceipts } from '../helpers/turnEndSound'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, loginViaToken, openAgentViaUI, openWorkspace, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, sidebarLeaves, waitForAgentIdle, waitForControlBanner, waitForEditorDraft, waitForWorkspaceReady, workspaceChevron, workspaceRow } from '../helpers/ui'

/** Click the displayed question option. Its control and label forward selection to the native input. */
async function clickOption(page: Page, label: string) {
  const option = page.locator(`[data-testid="question-option-${label}"]`)
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
  await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
}

test.describe('Control Request - AskUserQuestion', () => {
  test('single question - select an option and submit', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Send a message that triggers AskUserQuestion
    await askQuestions(page, modelScript, [COLOR_Q_3])

    // Wait for the control banner
    const banner = await waitForControlBanner(page)

    // Verify question text and options (scoped to banner to avoid matching chat messages)
    await expect(banner.getByText('Pick a color')).toBeVisible()
    await expect(page.locator('[data-testid="question-option-Red"]')).toBeVisible()
    await expect(page.locator('[data-testid="question-option-Blue"]')).toBeVisible()
    await expect(page.locator('[data-testid="question-option-Green"]')).toBeVisible()

    // Click "Blue" option
    await clickOption(page, 'Blue')

    // Verify Stop and Submit buttons are visible
    await expect(page.locator('[data-testid="control-stop-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="control-submit-btn"]')).toBeVisible()

    // Wait for Submit to become enabled, then click it
    const submitBtn = page.locator('[data-testid="control-submit-btn"]')
    await expect(submitBtn).toBeEnabled()
    await submitBtn.click()

    await waitForAgentIdle(page)
    const status = await modelScript.status()
    const answerRequest = status.requests.find(request => request.fallback === true)
    expect(answerRequest?.protocol).toBe('anthropic-messages')
    const answer = nativeToolResult(answerRequest, 'ask-user')
    expect(answer).toContain('Blue')
    expect(answer).not.toContain('Red color')
    expect(answer).not.toContain('Green color')
  })

  test('multi-question - pagination with option selection', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Send a message with 2 questions
    const before = await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

    const banner = await waitForControlBanner(page)

    // Verify only question 1 is shown (scoped to banner)
    await expect(banner.getByText('Pick a color')).toBeVisible()
    await expect(banner.getByText('Pick a size')).not.toBeVisible()

    // Verify pagination shows 2 page items
    const pagination = page.locator('[data-testid="control-pagination"]')
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

    // Wait for Submit to become enabled, then click it
    const submitBtn = page.locator('[data-testid="control-submit-btn"]')
    await expect(submitBtn).toBeEnabled()
    await submitBtn.click()

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Large'])
  })

  test('multi-question - option click auto-advances to next page', async ({ page, authenticatedWorkspace, modelScript }) => {
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

    // Wait for Submit to become enabled, then click it
    const submitBtn = page.locator('[data-testid="control-submit-btn"]')
    await expect(submitBtn).toBeEnabled()
    await submitBtn.click()

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Large'])
  })

  test('YOLO button fills unanswered questions', async ({ page, authenticatedWorkspace, modelScript }) => {
    const before = await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

    await waitForControlBanner(page)

    // Answer only question 1
    await clickOption(page, 'Red')

    // YOLO stays available while the second question is unanswered.
    await expect(page.locator('[data-testid="control-yolo-btn"]')).toBeVisible()

    // Click YOLO
    await page.locator('[data-testid="control-yolo-btn"]').click()

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Go with the recommended option.'])
  })

  test('Stop button rejects the request', async ({ page, authenticatedWorkspace, modelScript }) => {
    await askQuestions(page, modelScript, [COLOR_Q_2])

    await waitForControlBanner(page)

    // Click Stop
    await page.locator('[data-testid="control-stop-btn"]').click()

    // Verify control banner disappears
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
  })

  test('multi-question control request stays on the correct agent tab', async ({ page, authenticatedWorkspace, modelScript }) => {
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTabs).toHaveCount(1)

    // Open a second agent tab; the new tab becomes active.
    await openAgentViaUI(page)
    await expect(agentTabs).toHaveCount(2)

    const firstAgentTab = agentTabs.first()
    const secondAgentTab = agentTabs.nth(1)

    // Trigger AskUserQuestion only on the second agent.
    await secondAgentTab.click()
    const before = await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

    const secondBanner = await waitForControlBanner(page)
    await expect(secondBanner.getByText('Pick a color')).toBeVisible()

    // Switch to the first agent and verify the control request did not leak there.
    await firstAgentTab.click()
    await expect(firstAgentTab).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Switch back to the second agent and complete the request there.
    await secondAgentTab.click()
    await expect(secondAgentTab).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator('[data-testid="control-banner"]')).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]').getByText('Pick a color')).toBeVisible()

    await clickOption(page, 'Red')
    await expect(page.locator('[data-testid="control-banner"]').getByText('Pick a size')).toBeVisible()
    await clickOption(page, 'Large')

    const submitBtn = page.locator('[data-testid="control-submit-btn"]')
    await expect(submitBtn).toBeEnabled()
    await submitBtn.click()

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Large'])
  })

  test('control request on a background agent tab badges it', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await openAgentViaUI(page)
    await expect(agentTabs).toHaveCount(2)

    // Hold the actual response until agent 2 is selected. The new question must notify the hidden agent's tab.
    const gate = 'claude-question-background-tab'
    await agentTabs.first().click()
    await askQuestions(page, modelScript, [COLOR_Q_2], { gate })
    try {
      await agentTabs.nth(1).click()
      await expect(agentTabs.nth(1)).toHaveAttribute('aria-selected', 'true')
      await modelScript.releaseGate(gate)

      await expect(agentTabs.first().locator('[data-testid="tab-notification"]')).toBeVisible()
      await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

      await agentTabs.first().click()
      await expect(agentTabs.first().locator('[data-testid="tab-notification"]')).not.toBeVisible()
      await waitForControlBanner(page)
    }
    finally {
      await modelScript.releaseGateIfHeld(gate)
    }
  })

  test('control request on a background workspace badges its tab when returned to', async ({ page, leapmuxServer, modelScript }) => {
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

        const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
        await expect(agentTabs).toHaveCount(2)
        // Hold the response until the other workspace becomes active. Agent 1 must retain its badge after return.
        await agentTabs.first().click()
        await askQuestions(page, modelScript, [COLOR_Q_2], { gate })
        await agentTabs.nth(1).click()
        await workspaceRow(page, ws1).click()
        await waitForWorkspaceReady(page)
        await modelScript.releaseGate(gate)

        // The sidebar must show the notification while the other workspace's tab strip stays off screen.
        const sidebarMarker = '[data-testid="sidebar-tab-notification"]'
        await expect(sidebarLeaves(page, ws2).locator(sidebarMarker)).toHaveCount(1)

        // A folded workspace shows the notification on its own row.
        // Its leaves remain mounted and an expanded child can retain visibility inside the clipped grid.
        // Use data-expanded to prove the workspace's actual fold state.
        await workspaceChevron(page, ws2).click()
        await expect(workspaceRow(page, ws2)).toHaveAttribute('data-expanded', 'false')
        await expect(workspaceRow(page, ws2).locator(sidebarMarker)).toBeVisible()

        // Return with agent 2 selected. Require agent 1's notification after its background question arrives.
        await expect(async () => {
          await workspaceRow(page, ws2).click()
          await waitForWorkspaceReady(page)
          expect(await agentTabs.first().locator('[data-testid="tab-notification"]').count()).toBe(1)
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

test.describe('Control Request Draft Persistence', () => {
  test('AskUserQuestion custom text draft survives page reload', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Trigger AskUserQuestion.
    await askColor(page, modelScript)

    // Wait for the control banner.
    await waitForControlBanner(page)

    // Type custom text in the editor.
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('my custom color answer', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'my custom color answer')

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear.
    const bannerAfterReload = page.locator('[data-testid="control-banner"]')
    await expect(bannerAfterReload).toBeVisible()

    // Verify the editor still contains the custom text.
    const restoredEditor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('my custom color answer')
  })

  test('control request draft is isolated from conversation draft', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Type a conversation draft first.
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
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
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toHaveText('')

    // Type control request draft text.
    const editorForCtrl = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editorForCtrl.click()
    await page.keyboard.type('control request draft text', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'control request draft text')

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear.
    const bannerAfterReload = page.locator('[data-testid="control-banner"]')
    await expect(bannerAfterReload).toBeVisible()

    // Verify editor contains the control request draft (not the conversation draft).
    const restoredEditor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('control request draft text')
    await expect(restoredEditor).not.toContainText('conversation draft text')
  })
})

claudeTest.describe('Agent Settings', () => {
  claudeTest('interrupt via control request', async ({ authenticatedClaudeWorkspace: authenticatedWorkspace, page, modelScript, leapmuxServer }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a quick message to ensure the agent is fully started
    await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(1)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedWorkspace.workspaceId }
    const agent = await currentNativeAgent(context)
    await askQuestions(page, modelScript, [COLOR_Q_2])
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Pick a color')
    // The open question holds the Worker in WAITING_FOR_USER. The move into that
    // state was the turn's settle edge (see agentActivity.store `apply`).
    await expect.poll(async () => (await nativeAgentById(context, agent.id))?.activityState).toBe(AgentActivityState.WAITING_FOR_USER)
    const after = await observeSettledReceipts(page)
    await banner.getByTestId('control-interrupt').click()
    await expect.poll(async () => (await nativeAgentById(context, agent.id))?.activityState).toBe(AgentActivityState.IDLE)
    await expect(banner).not.toBeVisible()
    await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
    // WAITING_FOR_USER to IDLE is not a settle edge, because the agent was not
    // working. The stop therefore rings no second alert and records no receipt.
    expect(await currentIdleReceipt(page, { agentId: agent.id, after })).toBeUndefined()
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    await expect.poll(async () => (await channel.callWorker(leapmuxServer.workerId, 'ListAgentInputQueue', ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, { agentId: agent.id })).snapshot?.paused).toBe(true)
    await resumeInterruptedQueue(context)

    // Verify the agent is still responsive after interrupt by sending another message
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(3)
    await expectAssistantAnswer(page)
  })
})
