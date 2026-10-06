import { expect } from '@playwright/test'

import { isObject } from '../../../src/lib/jsonPick'
import { KIRO_MOCK_MODELS } from '../helpers/kiroSurface'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, expectNoSettingsChip, expectSettingsChip, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

const [EFFORT_MODEL, PLAIN_MODEL] = KIRO_MOCK_MODELS

/** The body of a Kiro request, which the mock records as an object. */
function requestBody(body: unknown): Record<string, unknown> {
  if (!isObject(body))
    throw new Error('The native Kiro request has no object body.')
  return body
}

/** The model a Kiro turn states, in its current message. */
function requestModel(body: Record<string, unknown>): unknown {
  const state = body.conversationState as { currentMessage?: { userInputMessage?: { modelId?: unknown } } } | undefined
  return state?.currentMessage?.userInputMessage?.modelId
}

kiroTest.describe('Kiro settings', () => {
  kiroTest('switches the effort, the mode and the model for the next prompt, and keeps them after reload', async ({ native, page }) => {
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')
    // The open request stated the effort, which differs from the model's own default.
    await expectSettingsChip(page, 'Medium')

    await chooseSettingsOption(page, 'effortLevel-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    const planned = requestBody((await sendNativeAnswer(native, 'Describe the plan in one word.', 'SETTINGS_APPLIED')).body)
    expect(planned.agentMode).toBe('plan')
    expect(planned.additionalModelRequestFields).toEqual({ output_config: { effort: 'high' } })
    expect(requestModel(planned)).toBe(EFFORT_MODEL!.modelId)

    // A model with no effort axis drops the axis, and its turns state no effort.
    await chooseSettingsOption(page, `model-${PLAIN_MODEL!.modelId}`)
    await waitForSettingsIdle(page)
    await expectNoSettingsChip(page, 'High')
    const switched = requestBody((await sendNativeAnswer(native, 'Answer with the other model.', 'MODEL_SWITCHED')).body)
    expect(requestModel(switched)).toBe(PLAIN_MODEL!.modelId)
    expect(switched).not.toHaveProperty('additionalModelRequestFields')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsOptionChosen(page, `model-${PLAIN_MODEL!.modelId}`)
    await expectNoSettingsChip(page, 'High')
    const restored = await sendNativeAnswer(native, 'Reply after restoring the selected native settings.', 'The restored Kiro settings answered.')
    expect(restored.body).toHaveProperty('agentMode', 'plan')
    expect(restored.body).toHaveProperty('conversationState.currentMessage.userInputMessage.modelId', PLAIN_MODEL!.modelId)
    expect(restored.body).not.toHaveProperty('additionalModelRequestFields')
  })
})
