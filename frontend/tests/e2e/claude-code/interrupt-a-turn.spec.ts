import { claudeTest } from '../claude-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

for (const kind of ['model', 'tool'] as const) {
  claudeTest(`interrupts a held native ${kind} turn and preserves the usable session`, async ({ native }) => {
    await exerciseInterruptTurn(native, { kind })
  })
}

claudeTest('interrupt via control request', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})
