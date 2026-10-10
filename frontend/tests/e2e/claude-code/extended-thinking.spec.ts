import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest as test } from '../claude-fixtures'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, openSettingsMenu, visibleOnly, waitForSettingsIdle } from '../helpers/ui'

test.describe('Agent Settings', () => {
  test('Extended Thinking label reflects model', async ({ authenticatedClaudeWorkspace, page }) => {
    void authenticatedClaudeWorkspace
    const thinkingMenu = async () => {
      const menu = await openSettingsMenu(page, 'alwaysThinkingEnabled')
      return { on: menu.getByTestId('alwaysThinkingEnabled-on'), off: menu.getByTestId('alwaysThinkingEnabled-off') }
    }

    // Sonnet supports adaptive thinking. The option ID stays "on" when its label changes.
    const sonnet = await thinkingMenu()
    await expect(sonnet.on).toBeVisible()
    await expect(sonnet.on).toContainText('Adaptive')
    await expect(sonnet.off).toBeVisible()
    await expect(sonnet.off).toContainText('Off')

    // Select Off and On. Check the confirmed choice after each change.
    await sonnet.off.click()
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'alwaysThinkingEnabled-off')
    await (await thinkingMenu()).on.click()
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'alwaysThinkingEnabled-on')

    // Haiku uses the On label. The native status update supplies the new option groups without a reload.
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)
    const haiku = await thinkingMenu()
    await expect(haiku.on).toContainText('On')
    await expect(haiku.on).not.toContainText('Adaptive')

    // Opus uses the Adaptive label.
    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, 'Opus')
    await waitForSettingsIdle(page)
    await expect((await thinkingMenu()).on).toContainText('Adaptive')
    await page.keyboard.press('Escape')
  })

  test('Extended Thinking toggle round-trip tracks the confirmed state', async ({ authenticatedClaudeWorkspace, page }) => {
    void authenticatedClaudeWorkspace
    const toggle = async (state: 'on' | 'off') => {
      await chooseSettingsOption(page, `alwaysThinkingEnabled-${state}`)
      await waitForSettingsIdle(page)
    }

    // On clears the CLI override. The confirmed state must return to On before the final Off change.
    await toggle('off')
    await toggle('on')
    await toggle('off')

    // Accept either Off or Adaptive-to-Off. The stored value controls the notification through firstSet.
    // Check the final state without requiring the initial notification before the first change.
    await expect(visibleOnly(page.getByText(/Extended Thinking \((?:.* → )?Off\)/))).toBeVisible()
    await expect(visibleOnly(page.getByText(/Extended Thinking \((?:.* → )?Adaptive\)/))).toHaveCount(0)
  })

  // Claude Code's model catalog refuses to disable thinking: 2.1.289 covered
  // Sonnet 5.5, Opus 5.5 and Fable, and 2.1.295 adds Haiku 5.5
  // (`rejects_disabled_thinking` in its capabilities), so every offered model
  // rejects the off state. A disabled session therefore sends NO `thinking` at
  // all -- the off state is proved by the absent key, not a `disabled` type.
  // Haiku 5.5 joins the adaptive models, so its enabled type is "adaptive" as
  // well; its own toggle still labels the state "On".
  test('applies thinking to the native request independently of model and effort, before and after reload', async ({ native, page }) => {
    const phases = [
      { model: 'model-sonnet', effort: 'effort-medium', modelPattern: /^claude-sonnet-/, states: ['on'] as const, enabledType: 'adaptive', expectedEffort: 'medium' },
      { model: 'model-haiku', effort: undefined, modelPattern: /^claude-haiku-/, states: ['off', 'on'] as const, enabledType: 'adaptive', expectedEffort: undefined },
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
        steps: phase.states.flatMap(state => [{ value: state, via: 'choose' as const }, { value: state, via: 'reload' as const }]),
        nativeProof: (request, step) => {
          expect(request.protocol).toBe('anthropic-messages')
          if (!isObject(request.body) || typeof request.body.model !== 'string')
            throw new Error('The native thinking request has no model ID.')
          selectedModel ??= request.body.model
          expect(request.body.model).toBe(selectedModel)
          expect(request.body.model).toEqual(expect.stringMatching(phase.modelPattern))
          if (step.value === 'on') {
            expect(request.body).toMatchObject({ thinking: { type: phase.enabledType } })
          }
          else {
            // The model refuses the disabled state, so the request states no thinking at all.
            expect('thinking' in request.body).toBe(false)
          }
          if (phase.expectedEffort !== undefined)
            expect(request.body).toMatchObject({ output_config: { effort: phase.expectedEffort } })
        },
      })
    }
  })
})
