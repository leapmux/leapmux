import { expect } from '@playwright/test'
import { claudeProcessTest as test } from '../claude-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, offeredSettingsOptions, openPlusMenu, settingsBar, settingsGroupTrigger, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { claudeUltracodeEnabled } from './ultracodeRequest'

test.describe('Agent Settings', () => {
  test('sends the selected effort in the next native request', async ({ native, page }) => {
    await expect(settingsBar(page)).toBeVisible()
    await exerciseNativeOption(native, {
      groupId: 'effort',
      value: 'high',
      nativeProof: request => expect(request.body).toMatchObject({ output_config: { effort: 'high' } }),
    })
  })

  // Claude 2.1.295 grants every offered model the same effort axis -- the CLI's own
  // rule excludes only opus-4-6, which its list no longer offers -- so the group no
  // longer hides for Haiku. Both surfaces must keep it, and the choice must survive
  // the trip through Haiku and back.
  test('every offered model keeps the effort group and the selected level', async ({ native, page }) => {
    await expect(settingsBar(page)).toBeVisible()

    const effortSubmenu = settingsGroupTrigger(page, 'effort')
    const effortChip = page.locator('[data-testid="composer-effort-trigger"]')

    const onSonnet = await offeredSettingsOptions(page, 'effort')
    await chooseSettingsOption(page, 'effort-xhigh')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-xhigh')

    for (const model of ['haiku', 'opus[1m]', 'sonnet']) {
      await chooseSettingsOption(page, `model-${model}`)
      await waitForSettingsIdle(page)
      // The group stays on both surfaces, and the menu offers the same levels.
      await openPlusMenu(page)
      await expect(effortSubmenu).toHaveCount(1)
      await expect(effortChip).toHaveCount(1)
      await page.keyboard.press('Escape')
      expect(await offeredSettingsOptions(page, 'effort')).toEqual(onSonnet)
      await expectSettingsOptionChosen(page, 'effort-xhigh')
    }

    const request = await sendNativeAnswer(native, 'Reply once after the model tour.', 'Claude answered after the tour.')
    expect(request.body).toMatchObject({ output_config: { effort: 'xhigh' } })
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
      const values = await offeredSettingsOptions(page, 'effort')
      expect(values, 'a capable model offers the automatic effort').toContain('auto')
      return values
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

  test('ultracode effort is selectable and keeps the agent working', async ({ native, page }) => {
    await expect(settingsBar(page)).toBeVisible()
    // The isolated native CLI supports Ultracode and applies xhigh with its harness instruction.
    await exerciseNativeOption(native, {
      groupId: 'effort',
      value: 'ultracode',
      prepare: async () => {
        await chooseSettingsOption(page, 'model-sonnet')
        await waitForSettingsIdle(page)
      },
      nativeProof: (request) => {
        expect(request.protocol).toBe('anthropic-messages')
        expect(request.body).toMatchObject({ model: expect.stringMatching(/^claude-sonnet-/), output_config: { effort: 'xhigh' } })
        expect(claudeUltracodeEnabled(request)).toBe(true)
      },
    })
  })

  // The user changes the model alone, so the switch carries no effort. The stored tier stays when
  // the new model offers it. Opus and Sonnet both offer xhigh in the installed CLI.
  test('a model switch keeps an effort that the new model supports', async ({ native, page }) => {
    await expect(settingsBar(page)).toBeVisible()
    await exerciseModelSwitchKeepsOption(native, {
      prepare: async () => {
        await chooseSettingsOption(page, 'model-opus[1m]')
        await expectSettingsChip(page, 'Opus')
        await waitForSettingsIdle(page)
      },
      kept: { groupId: 'effort', value: 'xhigh' },
      model: 'sonnet',
      nativeProof: request => expect(request.body).toMatchObject({
        model: expect.stringMatching(/^claude-sonnet-/),
        output_config: { effort: 'xhigh' },
      }),
    })
  })

  // No offered model lacks effort under the installed CLI, so nothing resets the
  // stored tier: a round trip through Haiku keeps the chosen level, and the next
  // native request proves it. Medium remains a real level of the shared set, so
  // choosing it after the trip proves the level follows the user, not the model.
  test('a round trip through Haiku keeps the selected effort', async ({ native, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'effort-xhigh')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-xhigh')

    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-xhigh')
    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-xhigh')

    const request = await sendNativeAnswer(native, 'Reply once after the round trip through Haiku.', 'Claude answered after the round trip.')
    expect(request.body).toMatchObject({ output_config: { effort: 'xhigh' } })
  })

  // A new session pins no effort: the CLI chooses the level of its model. The menu shows that level, and a
  // model switch must keep it. The CLI would otherwise choose the default of the new model, and the user
  // would see the effort change although only the model changed.
  test('a model switch keeps the level that the CLI chose for an automatic session', async ({ native, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    await chooseSettingsOption(page, 'model-sonnet')
    await expectSettingsChip(page, 'Sonnet')
    await waitForSettingsIdle(page)
    // Auto restarts the agent without --effort. Sonnet then runs at its own default, Medium.
    await chooseSettingsOption(page, 'effort-auto')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-medium')

    // Fable defaults to High, so a switch that keeps Medium differs from a switch that takes the default.
    // The effort menu chooses one level, so Medium also proves that High is not chosen.
    await chooseSettingsOption(page, 'model-fable[1m]')
    await expectSettingsChip(page, 'Fable')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'effort-medium')

    const request = await sendNativeAnswer(native, 'Reply once after the switch to Fable.', 'Claude answered on Fable.')
    expect(request.body).toMatchObject({
      model: expect.stringMatching(/^claude-fable-/),
      output_config: { effort: 'medium' },
    })
  })
})
