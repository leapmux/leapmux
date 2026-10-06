import { exerciseConversationContext } from '../helpers/nativeConversation'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code basic chat', () => {
  // The second prompt reaches the same MiMo session.
  // The model reads the first exchange from that session.
  mimoTest('continues the same session on a second prompt', async ({ native }) => {
    await exerciseConversationContext(native)
  })
})
