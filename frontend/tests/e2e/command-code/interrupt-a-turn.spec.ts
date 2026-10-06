import { commandCodeTest } from '../command-code-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

commandCodeTest('interrupts actual native model and tool turns without replacing the session', async ({ native }) => {
  await exerciseInterruptTurn(native)
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
