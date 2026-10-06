import { diracTest } from '../dirac-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

diracTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
