import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

codewhaleTest('proves native prompt delivery, turn completion, and saved rows', async ({ native }) => {
  await exerciseBasicChat(native)
})
