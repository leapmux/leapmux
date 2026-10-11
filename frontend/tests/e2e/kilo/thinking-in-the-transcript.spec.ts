import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { kiloTest } from '../kilo-fixtures'

kiloTest('keeps a thought before its answer after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
