import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests } from './scenarios'

reasonixTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { prepare: () => bypassToolRequests(native) })
})

reasonixTest('stops an actual native tool and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool', prepare: () => bypassToolRequests(native) })
})

// Reasonix asks no native question, so a permission request holds the turn. The ask approval mode raises one.
reasonixTest('withdraws a waiting permission and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseControlInterrupt(native, {
    control: 'permission',
    prepare: async () => {
      await chooseSettingsOption(native.page, 'tool_approval-ask')
      await waitForSettingsIdle(native.page)
    },
  })
})
