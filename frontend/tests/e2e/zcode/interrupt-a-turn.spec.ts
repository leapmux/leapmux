import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { zcodeTest } from '../zcode-fixtures'
import { bypassToolRequests } from './scenarios'

zcodeTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { prepare: () => bypassToolRequests(native) })
})

zcodeTest('stops an actual native tool and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool', prepare: () => bypassToolRequests(native) })
})

zcodeTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})

zcodeTest('withdraws a waiting question and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
