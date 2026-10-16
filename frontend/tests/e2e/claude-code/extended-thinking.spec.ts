import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest as test } from '../claude-fixtures'
import { currentNativeAgent, nativeOptionGroup } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, openSettingsMenu, visibleOnly, waitForSettingsIdle } from '../helpers/ui'

test.describe('Agent Settings', () => {
  test('shows the native thinking requirement for each model', async ({ native, page }) => {
    for (const [model, label] of [['sonnet', 'Adaptive'], ['haiku', 'On'], ['opus[1m]', 'Adaptive']] as const) {
      await chooseSettingsOption(page, `model-${model}`)
      await waitForSettingsIdle(page)
      const group = nativeOptionGroup(await currentNativeAgent(native), 'alwaysThinkingEnabled')
      expect(group).toMatchObject({ mutable: false, currentValue: 'on', readOnlyReason: 'This model requires extended thinking.' })
      const menu = await openSettingsMenu(page, 'alwaysThinkingEnabled')
      await expect(menu.getByTestId('alwaysThinkingEnabled-on')).toContainText(label)
      await expect(menu.getByTestId('alwaysThinkingEnabled-on')).toBeDisabled()
      await expect(menu.getByTestId('alwaysThinkingEnabled-off')).toHaveCount(0)
      await menu.getByTestId('alwaysThinkingEnabled-on').hover()
      await expect(page.getByRole('tooltip').filter({ hasText: 'This model requires extended thinking.' })).toBeVisible()
      await page.keyboard.press('Escape')
    }
  })

  test('keeps required thinking enabled across reload and model changes', async ({ native, page }) => {
    for (const model of ['haiku', 'sonnet', 'haiku']) {
      await chooseSettingsOption(page, `model-${model}`)
      await waitForSettingsIdle(page)
      await page.reload()
      await expectSettingsChip(page, model === 'haiku' ? 'Haiku' : 'Sonnet')
      const group = nativeOptionGroup(await currentNativeAgent(native), 'alwaysThinkingEnabled')
      expect(group?.currentValue).toBe('on')
      expect(group?.options.map(option => option.id)).toEqual(['on'])
      const menu = await openSettingsMenu(page, 'alwaysThinkingEnabled')
      await expect(menu.getByTestId('alwaysThinkingEnabled-on')).toHaveAttribute('aria-checked', 'true')
      await expect(menu.getByTestId('alwaysThinkingEnabled-on')).toBeDisabled()
      await page.keyboard.press('Escape')
    }
    await expect(visibleOnly(page.getByText(/Extended Thinking \((?:.* → )?Off\)/))).toHaveCount(0)
  })

  // Current native models require adaptive thinking, even when an old stored setting requests Off.
  test('keeps required thinking and selected effort in native requests before and after reload', async ({ native, page }) => {
    const phases = [
      { model: 'model-sonnet', effort: 'effort-medium', modelPattern: /^claude-sonnet-/, states: ['on'] as const, enabledType: 'adaptive', expectedEffort: 'medium' },
      { model: 'model-haiku', effort: undefined, modelPattern: /^claude-haiku-/, states: ['on'] as const, enabledType: 'adaptive', expectedEffort: undefined },
    ]
    for (const phase of phases) {
      await chooseSettingsOption(page, phase.model)
      await waitForSettingsIdle(page)
      if (phase.effort !== undefined) {
        await chooseSettingsOption(page, phase.effort)
        await waitForSettingsIdle(page)
      }
      let selectedModel: string | undefined
      await exerciseNativeOptionSequence(native, {
        groupId: 'alwaysThinkingEnabled',
        steps: phase.states.flatMap(state => [{ value: state, via: 'default' as const }, { value: state, via: 'reload' as const }]),
        nativeProof: (request, step) => {
          expect(request.protocol).toBe('anthropic-messages')
          if (!isObject(request.body) || typeof request.body.model !== 'string')
            throw new Error('The native thinking request has no model ID.')
          selectedModel ??= request.body.model
          expect(request.body.model).toBe(selectedModel)
          expect(request.body.model).toEqual(expect.stringMatching(phase.modelPattern))
          expect(step.value).toBe('on')
          expect(request.body).toMatchObject({ thinking: { type: phase.enabledType } })
          if (phase.expectedEffort !== undefined)
            expect(request.body).toMatchObject({ output_config: { effort: phase.expectedEffort } })
        },
      })
    }
  })
})
