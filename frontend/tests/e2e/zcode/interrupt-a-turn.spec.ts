import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { zcodeTest } from '../zcode-fixtures'
import { bypassToolRequests } from './scenarios'

zcodeTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { prepare: () => bypassToolRequests(native) })
})
