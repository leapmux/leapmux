import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

commandCodeTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
