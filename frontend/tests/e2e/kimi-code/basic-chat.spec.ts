import { exerciseBasicChat } from '../helpers/nativeConversation'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code for basic chat', () => {
  kimiTest('opens, sends a prompt, and receives a response', async ({ native }) => {
    // The native `turn.ended` event states the duration of the turn. In the first request of a session, Kimi Code sends
    // its own date reminder as a user row after the prompt.
    await exerciseBasicChat(native, {
      timedDivider: true,
      nativeRowsAfterPrompt: /<system-reminder>\nToday's date is \d{4}-\d{2}-\d{2}\. [^\n]*\n<\/system-reminder>/,
    })
  })
})
