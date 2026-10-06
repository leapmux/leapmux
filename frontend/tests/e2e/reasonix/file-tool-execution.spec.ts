import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { applyPermissionPreset } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('reads and changes actual scratch bytes through native file tools', async ({ native }) => {
  await exerciseFileToolExecution(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
