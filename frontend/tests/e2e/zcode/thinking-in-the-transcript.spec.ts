import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('keeps a thought before its answer after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
