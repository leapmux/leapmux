import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { museTest } from '../muse-fixtures'

museTest('keeps actual native output and exit codes after reload', async ({ native }) => {
  await exerciseShellToolExecution(native, { reload: true })
})
