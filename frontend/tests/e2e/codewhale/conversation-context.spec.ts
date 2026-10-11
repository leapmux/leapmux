import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

codewhaleTest.describe('Codewhale basic chat', () => {
  codewhaleTest('continues the same thread with a second message', async ({ native }) => {
    await exerciseConversationContext(native)
  })
})
