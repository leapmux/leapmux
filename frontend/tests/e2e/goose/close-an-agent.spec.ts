import { gooseTest } from '../goose-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { bypassToolRequests } from './scenarios'

gooseTest('closes the native agent and its actual owned process tree', async ({ native }) => {
  await exerciseCloseAgent(native, { prepare: () => bypassToolRequests(native) })
})
