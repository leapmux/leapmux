import { exerciseBasicChat } from '../helpers/nativeConversation'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code for basic chat', () => {
  kimiTest('opens, sends a prompt, and receives a response', async ({ native }) => {
    await exerciseBasicChat(native)
  })
})
