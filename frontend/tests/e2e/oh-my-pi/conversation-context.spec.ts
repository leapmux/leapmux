import { exerciseConversationContext } from '../helpers/nativeConversation'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * A second real native request must contain the earlier prompt and answer. Each turn keeps its own completion row.
 * This proves that Oh My Pi keeps the context between prompts in the same session.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi basic chat', () => {
  ohMyPiTest('keeps the conversation from one turn to the next', async ({ native }) => {
    await exerciseConversationContext(native)
  })
})
