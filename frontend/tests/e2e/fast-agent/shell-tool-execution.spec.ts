import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'

fastAgentTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native, { absentRowText: ['[Exit code:'] })
})
