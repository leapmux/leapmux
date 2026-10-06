import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { applyPermissionPreset } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
