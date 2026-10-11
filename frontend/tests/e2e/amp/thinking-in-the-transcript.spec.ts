import { ampTest } from '../amp-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

ampTest('keeps native thinking separate from the answer and restores it after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
