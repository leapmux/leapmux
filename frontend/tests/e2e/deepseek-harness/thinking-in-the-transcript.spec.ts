import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

deepseekHarnessTest('shows distinct native reasoning and answer blocks before and after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
