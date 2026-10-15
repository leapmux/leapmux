import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

opencodeTest('stops an actual native tool and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

opencodeTest('withdraws a waiting question and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})

opencodeTest('keeps interrupted partial text and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})
