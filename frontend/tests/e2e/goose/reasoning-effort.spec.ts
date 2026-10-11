import { expect } from '@playwright/test'
import { GOOSE_CONFIG } from '../../../src/generated/contracts/goose-protocol'
import { gooseTest } from '../goose-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseGooseModelAndEffort } from './settingsScenario'

gooseTest('keeps the high effort after a turn and reload', async ({ native }) => {
  await exerciseGooseModelAndEffort(native, 'effort')
})

// Goose keeps the raw `thinking_effort` value on a model write (`goose/settings_test.go`). The two reasoning models
// both offer Low, and the request of the new model must carry it.
gooseTest('keeps the chosen effort after a model switch and a reload', async ({ native, page }) => {
  await exerciseModelSwitchKeepsOption(native, {
    prepare: async () => {
      await chooseSettingsOption(page, `model-${MOCK_MODELS.gooseReasoning}`)
      await waitForSettingsIdle(page)
    },
    kept: { groupId: GOOSE_CONFIG.ThinkingEffort, value: 'low' },
    model: MOCK_MODELS.openai,
    nativeProof: (request) => {
      expect(request.protocol).toBe('openai-responses')
      expect(request.body).toMatchObject({ model: MOCK_MODELS.openai, reasoning: { effort: 'low' } })
    },
  })
})
