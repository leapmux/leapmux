import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('reads and changes actual scratch bytes through native file tools', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
