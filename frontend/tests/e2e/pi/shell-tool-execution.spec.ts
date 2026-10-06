import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { piTest } from '../pi-fixtures'

piTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
