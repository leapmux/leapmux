import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import type { QuestionRequest } from '../helpers/providerToolCalls'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { ASKED_QUESTIONS_CALL_ID, askQuestions, pickQuestionOption } from '../helpers/nativeQuestion'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { agentTabs, collapseWorkspaceRow, composerEditor, controlBanner, controlButton, expectNoControlBanner, focusComposer, loginViaToken, openAgentViaUI, openWorkspace, PLATFORM_MOD, questionPagination, sidebarLeaves, waitForAgentIdle, waitForControlBanner, waitForEditorDraft, waitForWorkspaceReady, workspaceRow, workspaceRowTitle } from '../helpers/ui'

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

/** The question context of the agent that these tests open: a Claude Code agent that this test's script drives. */
function claude(page: Page, modelScript: ModelScript): NativeScenarioContext {
  return { page, modelScript, provider: AgentProvider.CLAUDE_CODE }
}

/** Prove selected answers in the new native call-ID result, then require completion in the browser. */
async function expectQuestionAnswers(page: Page, script: ModelScript, before: number, values: readonly string[]): Promise<void> {
  await waitForAgentIdle(page)
  const status = await script.status()
  const request = status.requests.slice(before + 1).find(record => record.fallback === true)
  if (!request)
    throw new Error('The question answer reached no following native model request.')
  expect(request.protocol).toBe('anthropic-messages')
  const answer = nativeToolResult(request, ASKED_QUESTIONS_CALL_ID)
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
    const before = await askQuestions(claude(page, modelScript), [COLOR_Q_3])

    // Wait for the control banner
    const banner = await waitForControlBanner(page)

    // Verify question text and options (scoped to banner to avoid matching chat messages)
    await expect(banner.getByText('Pick a color')).toBeVisible()
    await expect(banner.getByTestId('question-option-Red')).toBeVisible()
    await expect(banner.getByTestId('question-option-Blue')).toBeVisible()
    await expect(banner.getByTestId('question-option-Green')).toBeVisible()

    // Click "Blue" option
    await pickQuestionOption(controlBanner(page), 'Blue')

    // Verify Stop and Submit buttons are visible
    await expect(controlButton(page, 'stop')).toBeVisible()
    await expect(controlButton(page, 'submit')).toBeVisible()

    await submitAnswers(page)

    await waitForAgentIdle(page)
    const status = await modelScript.status()
    const answerRequest = status.requests.slice(before).find(request => request.fallback === true)
    expect(answerRequest?.protocol).toBe('anthropic-messages')
    const answer = nativeToolResult(answerRequest, ASKED_QUESTIONS_CALL_ID)
    expect(answer).toContain('Blue')
    expect(answer).not.toContain('Red color')
    expect(answer).not.toContain('Green color')
  })

  claudeTest('multi-question - pagination with option selection', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Send a message with 2 questions
    const before = await askQuestions(claude(page, modelScript), [COLOR_Q_2, SIZE_Q])

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
    await pickQuestionOption(controlBanner(page), 'Red')

    // Verify question 2 is now shown (scoped to banner)
    await expect(banner.getByText('Pick a size')).toBeVisible()
    await expect(banner.getByText('Pick a color')).not.toBeVisible()

    // Answer question 2 by clicking "Large"
    await pickQuestionOption(controlBanner(page), 'Large')

    await submitAnswers(page)

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Large'])
  })

  claudeTest('multi-question - option click auto-advances to next page', async ({ page, authenticatedWorkspace, modelScript }) => {
    const before = await askQuestions(claude(page, modelScript), [COLOR_Q_2, SIZE_Q])

    const banner = await waitForControlBanner(page)

    // Verify page 1 shown (scoped to banner)
    await expect(banner.getByText('Pick a color')).toBeVisible()

    // Click "Red" -- should auto-advance
    await pickQuestionOption(controlBanner(page), 'Red')

    // Verify auto-advanced to page 2 (scoped to banner)
    await expect(banner.getByText('Pick a size')).toBeVisible()

    // Click "Large" on page 2 -- should stay on page 2 (last page)
    await pickQuestionOption(controlBanner(page), 'Large')
    await expect(banner.getByText('Pick a size')).toBeVisible()

    await submitAnswers(page)

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Large'])
  })

  claudeTest('YOLO button fills unanswered questions', async ({ page, authenticatedWorkspace, modelScript }) => {
    const before = await askQuestions(claude(page, modelScript), [COLOR_Q_2, SIZE_Q])

    await waitForControlBanner(page)

    // Answer only question 1
    await pickQuestionOption(controlBanner(page), 'Red')

    // YOLO stays available while the second question is unanswered.
    const yolo = controlButton(page, 'yolo')
    await expect(yolo).toBeVisible()

    // Click YOLO
    await yolo.click()

    await expectQuestionAnswers(page, modelScript, before, ['Red', 'Go with the recommended option.'])
  })

  claudeTest('Stop button rejects the request', async ({ page, authenticatedWorkspace, modelScript }) => {
    await askQuestions(claude(page, modelScript), [COLOR_Q_2])

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
    const before = await askQuestions(claude(page, modelScript), [COLOR_Q_2, SIZE_Q])

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

    await pickQuestionOption(controlBanner(page), 'Red')
    await expect(banner.getByText('Pick a size')).toBeVisible()
    await pickQuestionOption(controlBanner(page), 'Large')

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
    await askQuestions(claude(page, modelScript), [COLOR_Q_2], { gate })
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
    // The per-test reset of the suite hub deletes both workspaces, so the test deletes neither.
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Active')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Background')
    const gate = 'claude-question-background-workspace'
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, ws1)
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, ws2)
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, ws2)
    await withCleanup(async () => {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, ws2)
      await waitForWorkspaceReady(page)

      const tabs = agentTabs(page)
      await expect(tabs).toHaveCount(2)
      // Hold the response until the other workspace becomes active. Agent 1 must retain its badge after return.
      await tabs.first().click()
      await askQuestions(claude(page, modelScript), [COLOR_Q_2], { gate })
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
  })
})

claudeTest.describe('Control Request Draft Persistence', () => {
  claudeTest('AskUserQuestion custom text draft survives page reload', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Trigger AskUserQuestion.
    await askQuestions(claude(page, modelScript), [COLOR_Q_3])

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
    await page.keyboard.press(`${PLATFORM_MOD}+a`)
    await page.keyboard.press('Backspace')
    await askQuestions(claude(page, modelScript), [COLOR_Q_3])

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
