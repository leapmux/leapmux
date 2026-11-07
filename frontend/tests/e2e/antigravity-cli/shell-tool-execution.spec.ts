import { antigravityTest } from '../antigravity-fixtures'
import { exerciseAntigravityShellToolExecution } from './shellScenarios'

antigravityTest('runs native shell output and failed commands', async ({ native }) => {
  await exerciseAntigravityShellToolExecution(native)
})
