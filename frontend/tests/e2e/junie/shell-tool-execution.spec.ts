import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { junieTest } from '../junie-fixtures'

junieTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
