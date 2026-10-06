import { grokTest } from '../grok-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('draws model reasoning in a thought row', async ({ native }) => {
    await exerciseThinkingRows(native)
  })
})
