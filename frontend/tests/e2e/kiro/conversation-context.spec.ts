import { exerciseConversationContext } from '../helpers/nativeConversation'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro basic chat', () => {
  // Kiro sends the history of the session with each turn.
  kiroTest('continues the conversation in the same session', async ({ native }) => {
    await exerciseConversationContext(native)
  })
})
