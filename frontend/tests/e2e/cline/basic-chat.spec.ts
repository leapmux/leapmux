import { clineTest } from '../cline-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

clineTest('proves native prompt delivery, turn completion, and saved rows', async ({ native }) => {
  await exerciseBasicChat(native)
})
