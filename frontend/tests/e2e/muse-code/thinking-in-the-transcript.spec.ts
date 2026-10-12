import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { museTest } from '../muse-fixtures'

museTest('shows native reasoning and answer rows before and after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
