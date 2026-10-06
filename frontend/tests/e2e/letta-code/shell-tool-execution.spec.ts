import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { lettaTest } from '../letta-fixtures'

lettaTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
