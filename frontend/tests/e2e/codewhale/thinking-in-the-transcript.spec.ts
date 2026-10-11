import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

codewhaleTest.describe('Codewhale basic chat', () => {
  codewhaleTest('answers a message and keeps its thinking after a reload', async ({ native }) => {
    // The `deepseek` route reads `reasoning_content` as thinking, so the
    // reasoning reaches the transcript as its own row rather than as answer text.
    // A route that is not known to reason merges the reasoning into the answer text instead.
    await exerciseThinkingRows(native)
  })
})
