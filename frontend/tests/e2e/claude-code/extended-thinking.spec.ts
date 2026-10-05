import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest as test } from '../claude-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, expectSettingsChip, openSettingsMenu, visibleOnly, waitForSettingsIdle } from '../helpers/ui'

test.describe('Agent Settings', () => {
  test('Extended Thinking label reflects model', async ({ authenticatedClaudeWorkspace, page }) => {
    void authenticatedClaudeWorkspace
    const onOpt = page.locator('[data-testid="alwaysThinkingEnabled-on"]')
    const offOpt = page.locator('[data-testid="alwaysThinkingEnabled-off"]')

    // Sonnet supports adaptive thinking. The option ID stays "on" when its label changes.
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(onOpt).toBeVisible()
    await expect(onOpt).toContainText('Adaptive')
    await expect(offOpt).toBeVisible()
    await expect(offOpt).toContainText('Off')

    // Select Off and On. Check the confirmed radio state after each change.
    await offOpt.click()
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(page.locator('[data-testid="alwaysThinkingEnabled-off"] input[type="radio"]')).toBeChecked()
    await onOpt.click()
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(page.locator('[data-testid="alwaysThinkingEnabled-on"] input[type="radio"]')).toBeChecked()

    // Haiku uses the On label. The native status update supplies the new option groups without a reload.
    await chooseSettingsOption(page, 'model-haiku')
    await expectSettingsChip(page, 'Haiku')
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(onOpt).toContainText('On')
    await expect(onOpt).not.toContainText('Adaptive')

    // Opus uses the Adaptive label.
    await chooseSettingsOption(page, 'model-opus[1m]')
    await expectSettingsChip(page, 'Opus')
    await waitForSettingsIdle(page)
    await openSettingsMenu(page, 'alwaysThinkingEnabled')
    await expect(onOpt).toContainText('Adaptive')
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

  // Claude Code 2.1.289 refuses to disable thinking for Sonnet 5.5, Opus 5.5 and
  // Fable (`rejects_disabled_thinking` in its model catalog): for a disabled
  // session it sends no `thinking` at all, and its own toggle reports "Thinking
  // can't be turned off". So the Sonnet phase proves the enabled state with its
  // effort, and the off state is proved on Haiku 4.5, which accepts both states.
  // Haiku thinks with a token budget, so its enabled type is "enabled".
  test('applies thinking to the native request independently of model and effort, before and after reload', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
    void authenticatedClaudeWorkspace
    const context = { page, modelScript, provider: AgentProvider.CLAUDE_CODE }
    const phases = [
      { model: 'model-sonnet', effort: 'effort-medium', modelPattern: /^claude-sonnet-/, states: ['on'] as const, enabledType: 'adaptive', expectedEffort: 'medium' },
      { model: 'model-haiku', effort: undefined, modelPattern: /^claude-haiku-/, states: ['off', 'on'] as const, enabledType: 'enabled', expectedEffort: undefined },
    ]
    for (const phase of phases) {
      await chooseSettingsOption(page, phase.model)
      await waitForSettingsIdle(page)
      if (phase.effort !== undefined) {
        await chooseSettingsOption(page, phase.effort)
        await waitForSettingsIdle(page)
      }
      let selectedModel: string | undefined
      for (const state of phase.states) {
        await chooseSettingsOption(page, `alwaysThinkingEnabled-${state}`)
        await waitForSettingsIdle(page)
        for (const restored of [false, true]) {
          if (restored)
            await page.reload()
          await openSettingsMenu(page, 'alwaysThinkingEnabled')
          await expect(page.locator(`[data-testid="alwaysThinkingEnabled-${state}"] input[type="radio"]`)).toBeChecked()
          await page.keyboard.press('Escape')
          const request = await sendNativeAnswer(context, `Reply with thinking ${state} on ${phase.model} after reload ${restored}.`, `Thinking ${state} reached the native ${phase.model} turn after reload ${restored}.`)
          expect(request.protocol).toBe('anthropic-messages')
          if (!isObject(request.body) || typeof request.body.model !== 'string')
            throw new Error('The native thinking request has no model ID.')
          selectedModel ??= request.body.model
          expect(request.body.model).toBe(selectedModel)
          expect(request.body.model).toEqual(expect.stringMatching(phase.modelPattern))
          expect(request.body).toMatchObject({ thinking: { type: state === 'on' ? phase.enabledType : 'disabled' } })
          if (phase.expectedEffort !== undefined)
            expect(request.body).toMatchObject({ output_config: { effort: phase.expectedEffort } })
        }
      }
    }
  })
})
