import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'

/** A Codex model whose ladder lacks Max, which the default model offers. */
const CODEX_MODEL_WITHOUT_MAX = 'gpt-5.5'

codexTest.describe('applies Codex session settings', () => {
  codexTest('sends the selected effort and keeps it after a reload', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'effort',
      value: 'low',
      nativeProof: request => expect(request.body).toMatchObject({ reasoning: { effort: 'low' } }),
    })
    await expectSettingsChip(native.page, /low/i)
  })
})

// Codex states the model and the effort again in each turn, so both must hold after a switch.
codexTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: 'gpt-5.6-sol',
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: 'gpt-5.6-sol', reasoning: { effort: 'low' } })
    },
  })
})

// The static Codex models carry effort tiers, and `model/list` replaces them with the live ladders
// (`codex/catalog.go`). The menu of the default model must be the same before and after a trip through a model
// whose ladder lacks Max. The Worker sends Auto with the switch to that model (`resetEffortToAutoIfUnsupported`), and
// Codex then reports the effort of the thread, the default of the model, Medium, which the switch back keeps.
codexTest('keeps one effort menu over a round trip through a model without the chosen level', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: MOCK_MODELS.openai,
    chosen: 'max',
    via: CODEX_MODEL_WITHOUT_MAX,
    viaEfforts: ['auto', 'xhigh', 'high', 'medium', 'low'],
    settled: 'medium',
    nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.openai, reasoning: { effort: 'medium' } }),
  })
})
