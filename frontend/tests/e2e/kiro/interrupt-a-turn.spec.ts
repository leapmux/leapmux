import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro interrupt, steering and process lifetime', () => {
  kiroTest('interrupts a running turn', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'model', prompt: 'Write a long report.', divider: /^Turn interrupted$/ })
  })
})
