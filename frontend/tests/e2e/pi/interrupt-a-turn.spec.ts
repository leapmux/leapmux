import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'

piTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

piTest('stops an actual native tool and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

piTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})

// Pi's question extension asks through a native dialog. The interrupt must release that dialog.
piTest('withdraws a waiting question and accepts a new prompt after queue resume', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
