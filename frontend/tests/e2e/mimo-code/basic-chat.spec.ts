import { exerciseBasicChat } from '../helpers/nativeConversation'
import { mimoTest } from '../mimo-fixtures'

mimoTest('proves native prompt delivery, turn completion, and saved rows', async ({ native }) => {
  await exerciseBasicChat(native)
})
