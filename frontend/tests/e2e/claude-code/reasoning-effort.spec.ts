import { expect } from '@playwright/test'
import { chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, expectSettingsOptionChosen, openPlusMenu, openSettingsMenu, sendMessage, settingsBar, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { processTest as test } from '../process-control-fixtures'
import { claudeUltracodeEnabled } from './ultracodeRequest'

test.describe('Agent Settings', () => {
  test('sends the selected effort in the next native request', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Select High and wait for the applied native value.
    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)
    // Reopen the menu to verify its selected radio.
    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-high"] input[type="radio"]')).toBeChecked()
    await page.keyboard.press('Escape')

    await modelScript.queue({ text: 'Claude answered at high effort.' })
    await sendMessage(page, modelScript.prompt('Reply once after the effort switch.'))
    const status = await modelScript.waitForSteps()
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ output_config: { effort: 'high' } })
    await expectAssistantAnswer(page, { answer: /Claude answered at high effort\./ })
    await waitForAgentIdle(page)
    await page.reload()
    await waitForSettingsHydrated(page)
    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-high"] input[type="radio"]')).toBeChecked()
    await page.keyboard.press('Escape')
    await modelScript.queue({ text: 'The restored high effort reached the next Claude turn.' })
    await sendMessage(page, modelScript.prompt('Reply once after restoring high effort.'))
    const restored = await modelScript.waitForSteps(2)
    expect(restored.requests.find(request => request.stepIndex === 1)?.body).toMatchObject({ output_config: { effort: 'high' } })
    await expectAssistantAnswer(page, { answer: /The restored high effort reached the next Claude turn\./ })
  })

  test('effort hidden when haiku selected', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Haiku offers no effort group or effort chip. Sonnet offers both controls.
    const effortSubmenu = settingsGroupTrigger(page, 'effort')
    const effortChip = page.locator('[data-testid="composer-effort-trigger"]')

    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-high"]')).toBeVisible()
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)

    // Effort is hidden for Haiku, on both surfaces.
    await openPlusMenu(page)
    await expect(effortSubmenu).toHaveCount(0)
    await expect(effortChip).toHaveCount(0)
    await page.keyboard.press('Escape')

    // Restore Sonnet and its effort group.
    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-high"]')).toBeVisible()
    await page.keyboard.press('Escape')
  })

  test('the effort menu is the same before and after a model round trip', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // The static and live catalogs must offer the same effort levels for capable models.
    // The old fallback offered Max alone, then the live catalog added Xhigh after a model change.
    // Compare the original and restored menus to prevent that change.
    const effortOptions = async (): Promise<string[]> => {
      await waitForSettingsHydrated(page)
      await openSettingsMenu(page, 'effort')
      await expect(page.locator('[data-testid="effort-auto"]')).toBeVisible()
      const ids = await page.locator('[data-testid^="effort-"]:visible').evaluateAll(els =>
        els.map(el => el.getAttribute('data-testid') ?? ''),
      )
      await page.keyboard.press('Escape')
      return ids
    }

    const onSonnet = await effortOptions()
    expect(onSonnet, 'Sonnet offers an effort menu').not.toHaveLength(0)

    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, 'Opus')
    await waitForSettingsIdle(page)
    expect(await effortOptions(), 'Opus offers the same tiers').toEqual(onSonnet)

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    expect(await effortOptions(), 'and Sonnet still does on the way back').toEqual(onSonnet)
  })

  test('ultracode effort is selectable and keeps the agent working', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    await chooseSettingsOption(page, 'model-sonnet')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page, 'effort')
    await expect(page.locator('[data-testid="effort-ultracode"]')).toBeVisible()
    await page.keyboard.press('Escape')

    // The isolated native CLI supports Ultracode and applies xhigh with its harness instruction.
    await chooseSettingsOption(page, 'effort-ultracode')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-ultracode')

    // A numeric marker can match the duration row. Require a distinct word in the actual answer.
    await modelScript.queue({ text: 'PINEAPPLE' })
    await sendMessage(page, modelScript.prompt('Reply with exactly the word PINEAPPLE and nothing else.'))
    const first = await modelScript.waitForSteps(1)
    const assertNative = (stepIndex: number, status: typeof first) => {
      const request = status.requests.find(record => record.stepIndex === stepIndex)
      expect(request?.protocol).toBe('anthropic-messages')
      expect(request?.body).toMatchObject({ model: expect.stringMatching(/^claude-sonnet-/), output_config: { effort: 'xhigh' } })
      if (!request)
        throw new Error('The native Ultracode turn reached no model request.')
      expect(claudeUltracodeEnabled(request)).toBe(true)
    }
    assertNative(0, first)
    await expectAssistantAnswer(page, { answer: /\bPINEAPPLE\b/ })
    await waitForAgentIdle(page)
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, 'effort-ultracode')
    await modelScript.queue({ text: 'ULTRACODE_RESTORED' })
    await sendMessage(page, modelScript.prompt('Reply once after restoring Ultracode.'))
    assertNative(1, await modelScript.waitForSteps(2))
    await expectAssistantAnswer(page, { answer: /\bULTRACODE_RESTORED\b/ })
  })

  // The user changes the model alone, so the switch carries no effort. The stored tier stays when
  // the new model offers it. Opus and Sonnet both offer xhigh in the installed CLI.
  test('a model switch keeps an effort that the new model supports', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    const effortChecked = (tier: string) => page.locator(`[data-testid="effort-${tier}"] input[type="radio"]`)

    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, 'Opus')
    await waitForSettingsIdle(page)

    await chooseSettingsOption(page, 'effort-xhigh')
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'effort')
    await expect(effortChecked('xhigh')).toBeChecked()
    await page.keyboard.press('Escape')

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page, 'effort')
    await expect(effortChecked('xhigh')).toBeChecked()
    await expect(effortChecked('medium')).not.toBeChecked()
    await page.keyboard.press('Escape')

    await modelScript.queue({ text: 'Claude answered after the model switch.' })
    await sendMessage(page, modelScript.prompt('Reply once after switching to Sonnet.'))
    const status = await modelScript.waitForSteps()
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({
      model: expect.stringMatching(/^claude-sonnet-/),
      output_config: { effort: 'xhigh' },
    })
    await expectAssistantAnswer(page, { answer: /Claude answered after the model switch\./ })
    await waitForAgentIdle(page)

    // The saved selection survives a reload.
    await page.reload()
    await waitForSettingsHydrated(page)
    await openSettingsMenu(page, 'effort')
    await expect(effortChecked('xhigh')).toBeChecked()
    await page.keyboard.press('Escape')
  })

  // Haiku offers no effort axis, so the switch to Haiku drops the tier. The switch back to Sonnet
  // carries no effort either, and the row holds none, so Sonnet reports the level that it selects.
  test('a model switch to a model without effort resets the effort', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    const effortChecked = (tier: string) => page.locator(`[data-testid="effort-${tier}"] input[type="radio"]`)

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'effort-xhigh')
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'effort')
    await expect(effortChecked('xhigh')).toBeChecked()
    await page.keyboard.press('Escape')

    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page, 'effort')
    await expect(effortChecked('xhigh')).not.toBeChecked()
    await expect(effortChecked('medium')).toBeChecked()
    await page.keyboard.press('Escape')

    await modelScript.queue({ text: 'Claude answered after the round trip.' })
    await sendMessage(page, modelScript.prompt('Reply once after the round trip through Haiku.'))
    const status = await modelScript.waitForSteps()
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ output_config: { effort: 'medium' } })
    await expectAssistantAnswer(page, { answer: /Claude answered after the round trip\./ })
  })
})
