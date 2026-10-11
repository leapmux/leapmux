import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseRestoredNativeOption, waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsIdle } from '../helpers/ui'
import { exerciseCopilotPlanAndEffort } from './settingsScenario'

copilotTest('keeps Plan mode and low effort after a turn and reload', async ({ native }) => {
  await exerciseCopilotPlanAndEffort(native, 'effort')
})

// Both models offer High. The first model defaults to Medium, so the preserved tier differs from a reset.
copilotTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    prepare: async () => {
      await chooseSettingsOption(native.page, `model-${MOCK_MODELS.gooseReasoning}`)
      await waitForSettingsIdle(native.page)
    },
    kept: { groupId: 'effort', value: 'high' },
    model: MOCK_MODELS.openai,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.openai, reasoning_effort: 'high' })
    },
  })
})

// The mock catalog gives gpt-4o no reasoning tier, so Copilot offers no effort control (`copilot/catalog.go`).
// The Worker sends Auto with the switch to gpt-4o and with the switch back (`resetEffortToAutoIfUnsupported`).
// Copilot sends no tier for Auto (`copilot/settings.go`). The runtime keeps High when gpt-5.4 returns.
// The menu offers no Auto entry, so the round trip reaches an automatic session.
copilotTest('hides the effort for a model without levels and settles the runtime tier after the round trip', async ({ native }) => {
  // Confirm the destination model's default before this session chooses any effort.
  await waitForNativeOptionApplied(native, 'model', MOCK_MODELS.openai)
  await waitForNativeOptionApplied(native, 'effort', 'medium')
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: MOCK_MODELS.gooseReasoning,
    chosen: 'high',
    via: MOCK_MODELS.goose,
    viaEfforts: 'hidden',
    settled: 'high',
    nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.gooseReasoning, reasoning: { effort: 'high' } }),
  })
  const runtimeTier = nativeOptionValue(await currentNativeAgent(native), 'effort')
  if (runtimeTier === undefined)
    throw new Error('The Copilot runtime reports no automatic effort tier.')
  expect(runtimeTier).toBe('high')
  // The first model defaults to Medium. Its switch must preserve the reported High tier without an effort choice.
  await chooseSettingsOption(native.page, `model-${MOCK_MODELS.openai}`)
  await waitForSettingsIdle(native.page)
  await waitForNativeOptionApplied(native, 'model', MOCK_MODELS.openai)
  await waitForNativeOptionApplied(native, 'effort', runtimeTier)
  await expectSettingsOptionChosen(native.page, `effort-${runtimeTier}`)
  const proof = (request: { body: unknown }) => expect(request.body).toMatchObject({ model: MOCK_MODELS.openai, reasoning_effort: runtimeTier })
  proof(await sendNativeAnswer(native, 'Reply after the automatic effort model switch.', 'The runtime effort survived the model switch.'))
  await exerciseRestoredNativeOption(native, {
    groupId: 'effort',
    value: runtimeTier,
    nativeProof: proof,
  })
})
