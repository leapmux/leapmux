import { gooseTest } from '../goose-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { bypassToolRequests } from './scenarios'

gooseTest('reads and changes actual scratch bytes through native file tools', async ({ native }) => {
  await exerciseFileToolExecution(native, { prepare: () => bypassToolRequests(native) })
})
