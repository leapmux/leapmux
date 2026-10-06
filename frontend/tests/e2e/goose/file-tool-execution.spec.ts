import { gooseTest } from '../goose-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { applyPermissionPreset } from '../helpers/ui'

gooseTest('reads and changes actual scratch bytes through native file tools', async ({ native }) => {
  await exerciseFileToolExecution(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
