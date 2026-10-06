import { claudeTest } from '../claude-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

for (const kind of ['model', 'tool'] as const) {
  claudeTest(`interrupts a held native ${kind} turn and preserves the usable session`, async ({ native }) => {
    await exerciseInterruptTurn(native, { kind })
  })
}
