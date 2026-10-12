import { geminiTest } from '../gemini-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

geminiTest('preserves the native model thought in its own transcript row after reload', async ({ native }) => {
  await exerciseThinkingRows(native)
})
