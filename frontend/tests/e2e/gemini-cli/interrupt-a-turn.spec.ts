import { geminiTest } from '../gemini-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

for (const kind of ['model', 'tool'] as const) {
  geminiTest(`interrupts a held native ${kind} turn and keeps the session usable`, async ({ native }) => {
    await exerciseInterruptTurn(native, { kind })
  })
}

// Gemini CLI asks no native question, so a permission request holds the turn. It asks before each shell command.
geminiTest('withdraws a waiting permission and keeps the session usable', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'permission' })
})
