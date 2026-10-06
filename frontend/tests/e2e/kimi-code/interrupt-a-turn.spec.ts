import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { kimiTest } from '../kimi-fixtures'

kimiTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

kimiTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
