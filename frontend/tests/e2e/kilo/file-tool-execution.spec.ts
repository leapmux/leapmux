import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { kiloTest } from '../kilo-fixtures'

kiloTest('reads and changes actual scratch bytes through native file tools', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
