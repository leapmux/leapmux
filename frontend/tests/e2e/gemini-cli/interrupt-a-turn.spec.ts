import { geminiTest } from '../gemini-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

for (const kind of ['model', 'tool'] as const) {
  geminiTest(`interrupts a held native ${kind} turn and keeps the session usable`, async ({ native }) => {
    await exerciseInterruptTurn(native, { kind })
  })
}
