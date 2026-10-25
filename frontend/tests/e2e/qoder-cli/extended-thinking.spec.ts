import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { MOCK_MODELS, QODER_ALTERNATE_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption, exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsIdle } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'

/** The Extended Thinking group of the Qoder provider, and its values. */
const THINKING = 'thinking'
const THINKING_VALUE = { auto: 'auto', off: 'false', on: 'true' } as const
const THINKING_STATE = { [THINKING_VALUE.auto]: 'auto', [THINKING_VALUE.off]: 'off', [THINKING_VALUE.on]: 'on' } as const

/**
 * Require the thinking state that Qoder states in an OpenAI-compatible model request at effort Auto.
 * Off sends `--thinking disabled`. On sends `--thinking adaptive`. Auto sends no thinking flag, and at effort Auto
 * Qoder then states no thinking field at all. At a concrete effort Qoder turns thinking on by itself, so this check
 * needs effort Auto to tell Auto from On.
 */
function expectNativeThinkingAtAutomaticEffort(request: MockModelRequestRecord, thinking: 'auto' | 'off' | 'on'): void {
  if (thinking === 'auto') {
    expect(request.body).not.toHaveProperty('enable_thinking')
    return
  }
  expect(request.body).toMatchObject({ enable_thinking: thinking === 'on' })
}

qoderTest.describe('Qoder CLI extended thinking', () => {
  qoderTest('turns native thinking off and on before and after reload', async ({ native }) => {
    // The native session takes a launch flag after its first turn, as the effort spec states.
    await sendNativeAnswer(native, 'Reply once before the thinking setting changes.', 'The first turn answered.')
    await expectSettingsOptionChosen(native.page, 'effort-auto')
    await exerciseNativeOptionSequence(native, {
      groupId: THINKING,
      steps: [
        { value: THINKING_VALUE.auto, via: 'default' },
        { value: THINKING_VALUE.off, via: 'choose' },
        { value: THINKING_VALUE.off, via: 'reload' },
        { value: THINKING_VALUE.on, via: 'choose' },
        { value: THINKING_VALUE.auto, via: 'choose' },
      ],
      nativeProof: (request, step) => {
        expectNativeThinkingAtAutomaticEffort(request, THINKING_STATE[step.value as keyof typeof THINKING_STATE])
      },
    })
  })

  // Qoder turns thinking back on for an explicit effort, so Off must hold while the user changes the effort.
  qoderTest('keeps thinking Off when the effort changes, and sends the effort again when thinking returns', async ({ native }) => {
    const { page } = native
    const effortChip = page.locator('[data-testid="composer-effort-trigger"]:visible')
    // The native session takes a launch flag after its first turn, as the effort spec states.
    await sendNativeAnswer(native, 'Reply once before the reasoning settings change.', 'The first turn answered.')
    await chooseSettingsOption(page, `${THINKING}-${THINKING_VALUE.off}`)
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `${THINKING}-${THINKING_VALUE.off}`)
    await expect(effortChip).toContainText('Low')
    const off = await sendNativeAnswer(native, 'Reply once with thinking off at effort Low.', 'Thinking stayed off at effort Low.')
    expect(off.body).toMatchObject({ enable_thinking: false, reasoning_effort: 'none' })

    await chooseSettingsOption(page, `${THINKING}-${THINKING_VALUE.on}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `${THINKING}-${THINKING_VALUE.on}`)
    await expect(effortChip).toContainText('Low')
    const on = await sendNativeAnswer(native, 'Reply once with thinking on at effort Low.', 'Thinking returned at effort Low.')
    expect(on.body).toMatchObject({ enable_thinking: true, reasoning_effort: 'low' })
  })

  qoderTest('keeps thinking Off after a model switch and a reload', async ({ native }) => {
    await exerciseModelSwitchKeepsOption(native, {
      // The native session takes a launch flag after its first turn, as the effort spec states.
      prepare: async () => {
        await sendNativeAnswer(native, 'Reply once before thinking changes.', 'The first turn answered.')
      },
      kept: { groupId: THINKING, value: THINKING_VALUE.off },
      model: QODER_ALTERNATE_MODEL_ID,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: MOCK_MODELS.qoder })
        // The alternate mock model declares no reasoning, so Qoder may omit the field; it never turns thinking on.
        expect((request.body as Record<string, unknown>).enable_thinking ?? false).toBe(false)
      },
    })
  })
})
