import type { Page } from '@playwright/test'
import { CLINE_E2E_SKIP_REASON, clineTest, expect, offeredTools } from './cline-fixtures'
import { MOCK_MODELS } from './helpers/mockAgentEnvironment'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  chooseSettingsOption,
  closeComposerMenus,
  expectAssistantAnswer,
  expectSettingsChip,
  openPlusMenu,
  openSettingsMenu,
  SECOND_ARITHMETIC_ANSWER,
  SECOND_ARITHMETIC_ANSWER_TEXT,
  SECOND_ARITHMETIC_PROMPT,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

/**
 * 244 — Cline settings.
 *
 * Cline states no model catalog on its hub, so a session offers the models of the
 * worker's own table for the provider that the user's Cline settings select, and the
 * configured model. The isolated settings select `openai-compatible`, which the table
 * does not hold, so the configured model is the one offered, and it has no effort
 * ladder.
 *
 * Cline fixes a session's tools and system prompt when it creates the session. A
 * change between Plan and Act therefore creates the session again with the same id
 * and its stored messages, so the conversation goes on with the other mode's tools.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

/** The option ids the mode group offers. */
async function modeOptions(page: Page): Promise<string[]> {
  const group = await openSettingsMenu(page, 'permissionMode')
  // Each option also holds a label element whose id ends `-label`.
  const ids = await group.locator('[data-testid^="permissionMode-"]:not([data-testid$="-label"])').evaluateAll(nodes => nodes.map(node => node.getAttribute('data-testid') ?? ''))
  await closeComposerMenus(page)
  return ids
}

clineTest.describe('Cline settings', () => {
  clineTest('offers the configured model, the three modes, and no effort for a model without a ladder', async ({ askingClineWorkspace, page }) => {
    void askingClineWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, MOCK_MODELS.cline)
    await expectSettingsChip(page, 'Act')
    expect(await modeOptions(page)).toEqual(['permissionMode-plan', 'permissionMode-act', 'permissionMode-auto_approve'])

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toHaveCount(0)
    await closeComposerMenus(page)
  })

  clineTest('Shift+Tab toggles Plan mode from the composer', async ({ askingClineWorkspace, page }) => {
    void askingClineWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')

    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Act')
  })

  clineTest('moves the session between Act and Plan, and keeps the conversation', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await waitForSettingsHydrated(page)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    await chooseSettingsOption(page, 'permissionMode-act')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Act')

    await modelScript.queue({ text: 'Back in Act.' })
    await sendMessage(page, modelScript.prompt('Reply with the words: Back in Act.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const [act, plan, actAgain] = [0, 1, 2].map(index => status.requests.find(request => request.stepIndex === index)?.body)
    // Act offers the editor. Plan offers the plan tool in its place.
    expect(offeredTools(act)).toContain('editor')
    expect(offeredTools(act)).not.toContain('switch_to_act_mode')
    expect(offeredTools(plan)).toContain('switch_to_act_mode')
    expect(offeredTools(plan)).not.toContain('editor')
    expect(offeredTools(actAgain)).toContain('editor')
    expect(offeredTools(actAgain)).not.toContain('switch_to_act_mode')
    // Each new session holds the conversation of the session before it.
    expect(JSON.stringify(plan)).toContain(ARITHMETIC_ANSWER_TEXT)
    expect(JSON.stringify(actAgain)).toContain(ARITHMETIC_ANSWER_TEXT)
    expect(JSON.stringify(actAgain)).toContain(SECOND_ARITHMETIC_ANSWER_TEXT)
  })
})
