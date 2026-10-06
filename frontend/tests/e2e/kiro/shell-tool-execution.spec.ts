import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { kiroTest } from '../kiro-fixtures'

kiroTest('proves calculated native stdout and failed stderr reach the next turn', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
