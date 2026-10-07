import { grokTest } from '../grok-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('send message and receive response', async ({ native }) => {
    await exerciseBasicChat(native)
  })
})
