import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code basic chat', () => {
  // One scripted turn proves the native request path.
  // The Worker starts `mimo serve` and sends the prompt through HTTP.
  // It reads the answer from the event stream. The native idle status ends the turn.
  mimoTest('renders the reasoning and the answer, then clears the thinking indicator', async ({ native }) => {
    // The helper requires a thought row, not a bubble that holds the reasoning: an answer bubble
    // also holds the reasoning when MiMo merges the reasoning into the answer text.
    await exerciseThinkingRows(native)
  })
})
