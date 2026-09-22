import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import type { QuestionRequest } from './helpers/providerToolCalls'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { askUserQuestionToolCall } from './helpers/providerToolCalls'
import { loginViaToken, openAgentViaUI, openWorkspace, sendMessage, sidebarLeaves, waitForWorkspaceReady, workspaceChevron, workspaceRow } from './helpers/ui'

/** Wait for the control request banner to appear and return a scoped locator. */
async function waitForControlBanner(page: Page) {
  const banner = page.locator('[data-testid="control-banner"]')
  await expect(banner).toBeVisible()
  return banner
}

/**
 * Click an option radio/checkbox inside the control banner.
 * Kobalte's RadioGroup.Item outer div (which carries data-testid) has no
 * onClick handler.  Selection is triggered either through ItemControl
 * (visual circle) or ItemLabel (a real `<label for="...">` element).
 * We click the `<label>` which is the largest hit area and triggers
 * native label→input association, firing Kobalte's onChange.
 */
async function clickOption(page: Page, label: string) {
  const option = page.locator(`[data-testid="question-option-${label}"]`)
  await expect(option).toBeVisible()
  await option.click()
}

// The questions travel as a SCRIPTED tool call rather than as JSON inside a
// prompt, so the banner draws exactly these options every run. The earlier
// form asked the model to copy a JSON literal, and a model that rewrote it
// produced different labels than the assertions below name.

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

/**
 * Script one `AskUserQuestion` call and send the turn that makes it.
 *
 * The answer returns to the model, which then reports it — a turn whose count
 * depends on what the test does with the banner, so the fallback answers it.
 *
 * `holdMs` delays the ANSWER, which is what a test needs when the request must
 * arrive after something else it does. The endpoint replies in milliseconds, so
 * a test that raced a real model's latency now wins that race instead of losing
 * it, and gets the opposite of the state it meant to set up.
 */
async function askQuestions(
  page: Page,
  script: ModelScript,
  questions: QuestionRequest[],
  options: { holdMs?: number } = {},
): Promise<void> {
  await script.fallback({ text: 'You answered the questions.' })
  await script.queue({
    toolCalls: [askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'ask-user', questions)],
    ...(options.holdMs === undefined ? {} : { delayMs: options.holdMs }),
  })
  await sendMessage(page, script.prompt('Use AskUserQuestion and tell me what I answered.'))
  // A held answer has not reached the agent yet, so the caller decides when to
  // wait for the banner it raises.
  if (options.holdMs === undefined)
    await script.waitForSteps()
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

    // Wait for assistant response containing "Blue"
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Blue')
    })
  })

  test('multi-question - pagination with option selection', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Send a message with 2 questions
    await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

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

    // Wait for assistant response containing both answers
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Red') && body.includes('Large')
    })
  })

  test('multi-question - option click auto-advances to next page', async ({ page, authenticatedWorkspace, modelScript }) => {
    await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

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

    // Wait for response
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Red') && body.includes('Large')
    })
  })

  test('YOLO button fills unanswered questions', async ({ page, authenticatedWorkspace, modelScript }) => {
    await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

    await waitForControlBanner(page)

    // Answer only question 1
    await clickOption(page, 'Red')

    // Go back to page 1 to verify YOLO is visible while Q2 is unanswered
    // (auto-advanced to page 2, but we check YOLO on any page)
    await expect(page.locator('[data-testid="control-yolo-btn"]')).toBeVisible()

    // Click YOLO
    await page.locator('[data-testid="control-yolo-btn"]').click()

    // Wait for assistant response (YOLO auto-submits after filling)
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Red') && body.includes('recommended')
    })
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
    await askQuestions(page, modelScript, [COLOR_Q_2, SIZE_Q])

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

    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('Red') && body.includes('Large')
    })
  })

  test('control request on a background agent tab badges it', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await openAgentViaUI(page)
    await expect(agentTabs).toHaveCount(2)

    // Raise the control request on agent 1, then hide it behind agent 2 before
    // the banner can claim focus — the NOTIFY path must still light the badge.
    // The hold is what puts the request after the switch: without it the mock
    // endpoint answers first and the banner claims focus while agent 1 is still
    // selected, which is the state this test exists to avoid.
    await agentTabs.first().click()
    await askQuestions(page, modelScript, [COLOR_Q_2], { holdMs: 2_000 })
    await agentTabs.nth(1).click()
    await expect(agentTabs.nth(1)).toHaveAttribute('aria-selected', 'true')

    await expect(agentTabs.first().locator('[data-testid="tab-notification"]')).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    await agentTabs.first().click()
    await expect(agentTabs.first().locator('[data-testid="tab-notification"]')).not.toBeVisible()
    await waitForControlBanner(page)
  })

  test('control request on a background workspace badges its tab when returned to', async ({ page, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Active')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Background')
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, ws2)
      await waitForWorkspaceReady(page)

      const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(agentTabs).toHaveCount(2)
      // Leave agent 2 selected so agent 1 can keep its badge after we return.
      // The hold puts the request after the switch, so the banner never claims
      // focus while agent 1 is selected.
      await agentTabs.first().click()
      await askQuestions(page, modelScript, [COLOR_Q_2], { holdMs: 2_000 })
      await agentTabs.nth(1).click()
      await workspaceRow(page, ws1).click()
      await waitForWorkspaceReady(page)

      // The sidebar carries the marker too, and from here it is the only place
      // that shows it: ws2's tab strip is off screen. The retry is the NOTIFY
      // path arriving, the same one the tab-strip assertion below waits for.
      const sidebarMarker = '[data-testid="sidebar-tab-notification"]'
      await expect(sidebarLeaves(page, ws2).locator(sidebarMarker)).toHaveCount(1)

      // Folded away, the workspace row answers for the tab under it.
      //
      // The fold state comes from `data-expanded`, never from the leaves. A
      // folded row keeps them in the DOM, and neither a count nor a visibility
      // check can tell folded from open there: the grid clips the subtree to
      // zero height, but each leaf keeps its own box, and an expanded branch
      // group inside re-declares `visibility: visible` over the folded
      // ancestor. The row's own marker is the bit that really moves.
      await workspaceChevron(page, ws2).click()
      await expect(workspaceRow(page, ws2)).toHaveAttribute('data-expanded', 'false')
      await expect(workspaceRow(page, ws2).locator(sidebarMarker)).toBeVisible()

      // Return with agent 2 still active. Retry until the backgrounded agent's
      // control request has arrived (NOTIFY while we were on ws1).
      await expect(async () => {
        await workspaceRow(page, ws2).click()
        await waitForWorkspaceReady(page)
        expect(await agentTabs.first().locator('[data-testid="tab-notification"]').count()).toBe(1)
      }).toPass()

      // Activating the workspace expands it again, and the row hands the marker
      // back to the leaf it belongs to.
      await expect(workspaceRow(page, ws2)).toHaveAttribute('data-expanded', 'true')
      await expect(workspaceRow(page, ws2).locator(sidebarMarker)).toHaveCount(0)
      await expect(sidebarLeaves(page, ws2).locator(sidebarMarker)).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })
})
