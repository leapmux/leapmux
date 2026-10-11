import { cursorTest } from '../cursor-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

cursorTest('keeps a thought before its answer after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
