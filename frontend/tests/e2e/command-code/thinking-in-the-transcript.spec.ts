import { commandCodeTest } from '../command-code-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

commandCodeTest('shows native thinking and answer rows before and after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
