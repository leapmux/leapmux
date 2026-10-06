import { clineTest } from '../cline-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

/**
 * A second real native request must contain the earlier prompt and answer. Each turn keeps its own completion row.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.describe('Cline basic chat', () => {
  clineTest('keeps the conversation from one turn to the next', async ({ native }) => {
    await exerciseConversationContext(native)
  })
})
