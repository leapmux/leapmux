import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { GOOSE_CONFIG } from '../../../src/generated/contracts/goose-protocol'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'

/** Require the reasoning model and its high effort in a native Goose Responses request. */
function expectReasoningModelAtHighEffort(request: MockModelRequestRecord): void {
  expect(request.protocol).toBe('openai-responses')
  expect(request.body).toMatchObject({ model: MOCK_MODELS.gooseReasoning, reasoning: { effort: 'high' } })
}

/**
 * Keep Goose's reasoning model and its high effort together after a turn and a reload, and restore one of the two
 * axes. Goose's effort axis is its own `thinking_effort`, and the reasoning model offers it.
 */
export async function exerciseGooseModelAndEffort(context: ManagedNativeScenarioContext, restore: 'model' | 'effort'): Promise<void> {
  const { page } = context
  await chooseSettingsOption(page, `model-${MOCK_MODELS.gooseReasoning}`)
  await expectSettingsChip(page, MOCK_MODELS.gooseReasoning)
  await chooseSettingsOption(page, `${GOOSE_CONFIG.ThinkingEffort}-high`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'High')
  expectReasoningModelAtHighEffort(await sendNativeAnswer(context, 'Reply once after the settings change.', 'The Goose settings applied.'))
  await exerciseRestoredNativeOption(context, {
    ...(restore === 'model'
      ? { groupId: 'model', value: MOCK_MODELS.gooseReasoning }
      : { groupId: GOOSE_CONFIG.ThinkingEffort, value: 'high' }),
    nativeProof: expectReasoningModelAtHighEffort,
  })
  await expectSettingsChip(page, MOCK_MODELS.gooseReasoning)
  await expectSettingsChip(page, 'High')
}
