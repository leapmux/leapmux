import { copilotTest } from '../copilot-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'

copilotTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
