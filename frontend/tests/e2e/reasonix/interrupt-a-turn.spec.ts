import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests } from './scenarios'

reasonixTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { prepare: () => bypassToolRequests(native) })
})
