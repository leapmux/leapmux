import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The native reasoning block must appear in its transcript row. The answer remains a separate row.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi basic chat', () => {
  ohMyPiTest('draws the thinking of a reply as a row of its own, before the answer, also after a reload', async ({ native }) => {
    // omp reads `reasoning_content` as a thinking block, and states the whole reply
    // as ONE message that holds the thinking and the text. The worker persists the
    // thinking as a row of its own, so the saved transcript keeps it.
    await exerciseThinkingRows(native)
  })
})
