import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS, OH_MY_PI_ALT_MODEL_ID, OH_MY_PI_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The selected effort must reach an actual native request. The setting must survive a page reload.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * `set_thinking_level` changes native effort live. The approval mode changes at launch, so its change restarts the agent.
 * `oh-my-pi/mode.spec.ts` proves the approval mode itself, and `oh-my-pi/bypass-permissions-shortcut.spec.ts` proves
 * Bypass. This test proves that the thinking level survives the restart of a mode change.
 */
ohMyPiTest('applies Oh My Pi settings, keeps them over a restart and a reload, and sends the thinking level', async ({ native }) => {
  const { page, modelScript } = native
  await waitForSettingsHydrated(page)
  // The fixture opens the agent in omp's Yolo mode.
  await expectSettingsChip(page, 'Yolo')

  await chooseSettingsOption(page, 'effort-low')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Low')

  // A new approval mode restarts omp with the new `--approval-mode`.
  await chooseSettingsOption(page, 'permissionMode-always-ask')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Always Ask')
  await expectSettingsChip(page, 'Low')

  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Always Ask')
  await expectSettingsChip(page, 'Low')

  // The thinking level reaches the model after the restart.
  const lowStep = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps(lowStep + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  expect((await modelScript.requestAt(lowStep)).body).toHaveProperty('reasoning_effort', 'low')
})

ohMyPiTest('sends low native effort before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'effort', value: 'low', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})

// omp keeps the thinking level between reasoning models, and LeapMux folds the level that omp reports.
ohMyPiTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: OH_MY_PI_ALT_MODEL_ID,
    nativeProof: (request) => {
      expect(JSON.stringify(request.body)).toContain(`"model":"${OH_MY_PI_ALT_MODEL_WIRE_ID}"`)
      expect(request.body).toMatchObject({ reasoning_effort: 'low' })
    },
  })
})

// The alternate model lacks Max. omp keeps Max as its configured level: it runs the alternate model at Xhigh, the
// highest level that model offers, and it runs the default model at Max again when the model returns. LeapMux shows
// the level that omp reports (`ohmypi/settings.go`, `handleThinkingLevelChanged`), and the next request carries Max.
ohMyPiTest('keeps the configured level over a round trip through a model without it', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: `${MOCK_PROVIDER_IDS.ohMyPi}/${MOCK_MODELS.ohMyPi}`,
    chosen: 'max',
    via: OH_MY_PI_ALT_MODEL_ID,
    viaEfforts: ['auto', 'xhigh', 'high', 'medium', 'low', 'minimal', 'off'],
    settled: 'max',
    nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.ohMyPi, reasoning_effort: 'max' }),
  })
})
