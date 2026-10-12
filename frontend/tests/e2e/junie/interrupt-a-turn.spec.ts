import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'

junieTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

junieTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

// Junie sends its question as a permission request with one option for each choice, so that request holds the turn.
junieTest('withdraws a waiting permission-shaped question and accepts the next queued turn', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
