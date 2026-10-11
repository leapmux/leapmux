import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { piTest } from '../pi-fixtures'

piTest('draws the thinking of a reply as a row of its own, before the answer, also after a reload', async ({ native }) => {
  // Pi reads `reasoning_content` as a thinking block, and states the whole reply as
  // ONE message that holds the thinking and the text. The worker persists the
  // thinking as a row of its own, so the saved transcript keeps it.
  await exerciseThinkingRows(native)
})
