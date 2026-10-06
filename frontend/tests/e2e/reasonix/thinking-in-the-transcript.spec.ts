import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('keeps a thought before its answer after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
