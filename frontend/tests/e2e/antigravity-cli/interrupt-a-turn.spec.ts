import { antigravityTest } from '../antigravity-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'

for (const kind of ['model', 'tool'] as const) {
  antigravityTest(`interrupts a held native ${kind} turn and keeps the session usable`, async ({ native }) => {
    await exerciseInterruptTurn(native, { kind })
  })
}

// A native permission request holds the turn until the reader answers or interrupts it.
antigravityTest('withdraws a waiting permission and keeps the session usable', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'permission' })
})

antigravityTest('keeps interrupted partial text and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})
