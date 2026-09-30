import type { Page } from '@playwright/test'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseContextUsage } from './helpers/contextUsage'
import { bashToolCall } from './helpers/providerToolCalls'
import { applyPermissionPreset, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, bandRows, expectAssistantAnswer, expectSettingsChip, openPlusMenu, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

/** The thought band that holds a turn's reasoning. */
function thoughtBands(page: Page) {
  return page.locator('[data-band="thought"]:visible')
}

kimiTest.describe('uses Kimi Code for basic chat', () => {
  kimiTest('reports model usage in the agent info card', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await exerciseContextUsage(page, modelScript)
  })

  kimiTest('opens, sends a prompt, and receives a response', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
  })

  // The worker assembles the streamed reasoning and text into rows of its own,
  // so a reload reads the same rows that the live turn drew.
  kimiTest('draws the reasoning before the answer and keeps both after a reload', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    const reasoning = 'I add the two numbers column by column.'
    await modelScript.queue({ reasoning, text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)
    await expect(thoughtBands(page).filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)

    await page.reload()
    await expectAssistantAnswer(page)
    await expect(thoughtBands(page).filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })

  // Each prompt goes to the same kap-server session, so the second request
  // carries the first exchange.
  kimiTest('keeps one session across two turns', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)

    await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    const second = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(second?.body)).toContain(ARITHMETIC_ANSWER_TEXT)
    await expect(assistantBubbles(page).filter({ hasText: ARITHMETIC_ANSWER_TEXT })).not.toHaveCount(0)
  })
})

kimiTest.describe('applies Kimi Code permission presets', () => {
  kimiTest('starts on Always Ask and offers both shortcuts', async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toBeVisible()
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
  })

  kimiTest('the smart shortcut asks before a risky command and bypass runs it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    await waitForSettingsHydrated(page)
    // Kimi treats removal of one file as routine. Forced recursive removal of
    // this test directory reaches the Smart permission check.
    const directory = join(authenticatedKimiWorkspace.workingDir, 'shortcut-dangerous-proof')
    mkdirSync(directory)
    writeFileSync(join(directory, 'keep.txt'), 'delete only in bypass\n')

    await applyPermissionPreset(page, 'smart')
    await expectSettingsChip(page, 'Ask When Needed')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.KIMI_CODE, 'smart-delete', 'rm -r -f shortcut-dangerous-proof')] },
      { text: 'Smart kept the file.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted delete under Smart permissions.'))
    await modelScript.waitForSteps(1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('shortcut-dangerous-proof')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(existsSync(directory)).toBe(true)

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.KIMI_CODE, 'bypass-delete', 'rm -r -f shortcut-dangerous-proof')] },
      { text: 'Bypass removed the file.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted delete under Bypass permissions.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(existsSync(directory)).toBe(false)

    // The kap-server holds the mode, and the worker reads it back on reload.
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Never Ask')
  })
})
