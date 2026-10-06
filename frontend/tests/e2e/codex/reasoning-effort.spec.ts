import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'

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
