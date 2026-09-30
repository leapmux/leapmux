import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest, expect } from './cursor-fixtures'
import { askUserQuestionToolCall, cursorCreatePlanToolCall, cursorWebFetchPermissionToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsIdle } from './helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest.describe('Cursor native interactions', () => {
  cursorTest('sends the selected question answer to the native Run stream', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    await modelScript.queue({ toolCalls: [askUserQuestionToolCall(AgentProvider.CURSOR, 'cursor-color', [{
      header: 'Color',
      question: 'Which color should I use?',
      options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }],
    }])] })
    await sendMessage(page, modelScript.prompt('Ask which color to use.'))
    await modelScript.waitForSteps()
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Which color should I use?')
    await banner.getByTestId('question-option-Blue').click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
  })

  cursorTest('approves a native create-plan request', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await modelScript.queue({ toolCalls: [cursorCreatePlanToolCall(
      'cursor-plan',
      'Review changes',
      'Review without edits.',
      '# Plan\n\n1. Inspect the files.',
    )] })
    await sendMessage(page, modelScript.prompt('Write a plan and ask for approval.'))
    await modelScript.waitForSteps()
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Review changes')
    await expect(banner).toContainText('Inspect the files')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Cursor plan accepted' }).first()).toBeVisible()
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'Cursor plan accepted' }).first()).toBeVisible()
  })

  cursorTest('forwards a native web-fetch permission decision', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    await modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall('cursor-fetch', 'https://example.invalid/cursor-probe')] })
    await sendMessage(page, modelScript.prompt('Ask permission to fetch the scripted URL.'))
    await modelScript.waitForSteps()
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('https://example.invalid/cursor-probe')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch approved' }).first()).toBeVisible()
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch approved' }).first()).toBeVisible()
  })
})
