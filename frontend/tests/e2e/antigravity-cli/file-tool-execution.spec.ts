import { antigravityTest } from '../antigravity-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'

antigravityTest('reads and changes actual native files and shows the applied diff', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
