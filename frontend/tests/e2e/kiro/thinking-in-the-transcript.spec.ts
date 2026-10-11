import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('sends a message and receives the response', async ({ native }) => {
    // The thinking is a row of its own, and the answer row holds the answer alone.
    await exerciseThinkingRows(native)
  })
})
