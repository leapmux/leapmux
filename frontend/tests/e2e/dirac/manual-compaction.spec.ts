import { diracTest } from '../dirac-fixtures'
import { exerciseNativeCondense } from './compactionScenarios'

diracTest.describe('native manual compaction', () => {
  diracTest('runs the native smol command through its condense tool', async ({ native }) => {
    await exerciseNativeCondense(native)
  })
})
