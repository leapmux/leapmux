import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('draws model reasoning in a thought row', async ({ native }) => {
    await exerciseThinkingRows(native)
  })
})
