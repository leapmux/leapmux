import { exerciseBasicChat } from '../helpers/nativeConversation'
import { kiroTest } from '../kiro-fixtures'

kiroTest('proves native prompt delivery, turn completion, and saved rows', async ({ native }) => {
  await exerciseBasicChat(native)
})
