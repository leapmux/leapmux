import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
