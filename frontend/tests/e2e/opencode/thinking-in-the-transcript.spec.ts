import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('keeps a thought before its answer after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
