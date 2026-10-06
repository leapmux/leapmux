import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { zcodeTest } from '../zcode-fixtures'
import { bypassToolRequests } from './scenarios'

zcodeTest('closes the native agent and its actual owned process tree', async ({ native }) => {
  await exerciseCloseAgent(native, { prepare: () => bypassToolRequests(native) })
})
