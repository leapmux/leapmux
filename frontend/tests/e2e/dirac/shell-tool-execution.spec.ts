import { diracTest } from '../dirac-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

diracTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native, { absentRowText: ['Command executed successfully (exit code', 'Command failed with exit code', 'Output:'] })
})
