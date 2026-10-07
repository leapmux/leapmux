import { clineTest } from '../cline-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

clineTest('proves native prompt delivery, turn completion, and saved rows', async ({ native }) => {
  // The end event of a native run states no duration. The Worker measures the turn and adds the duration to the turn
  // end.
  await exerciseBasicChat(native, { timedDivider: true })
})
