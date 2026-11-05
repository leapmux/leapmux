import { antigravityTest } from '../antigravity-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

antigravityTest('preserves the native model thought in its own transcript row after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
