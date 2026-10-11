import { ampTest } from '../amp-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

/**
 * A second real native request must contain the earlier prompt and answer. Each turn keeps its own completion row.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp basic chat', () => {
  ampTest('keeps the conversation from one turn to the next', async ({ native }) => {
    await exerciseConversationContext(native)
  })
})
