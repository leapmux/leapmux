import { exerciseConversationContext } from '../helpers/nativeConversation'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code for basic chat', () => {
  // Each prompt goes to the same kap-server session, so the second request
  // carries the first exchange.
  kimiTest('keeps one session across two turns', async ({ native }) => {
    await exerciseConversationContext(native)
  })
})
