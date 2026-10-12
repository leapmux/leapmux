import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { museTest } from '../muse-fixtures'

museTest('reads and changes real files and keeps the native edit diff', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
