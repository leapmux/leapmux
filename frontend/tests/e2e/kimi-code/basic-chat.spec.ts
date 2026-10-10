import { exerciseBasicChat } from '../helpers/nativeConversation'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code for basic chat', () => {
  kimiTest('opens, sends a prompt, and receives a response', async ({ native }) => {
    // The native `turn.ended` event states the duration of the turn. In the first request of
    // a session, Kimi Code sends its own date reminder as a user row after the prompt, and
    // the turn reader classifies that row as context, so the prompt check needs no override.
    await exerciseBasicChat(native, { timedDivider: true })
  })
})
