import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

codewhaleTest('proves native prompt delivery, turn completion, and saved rows', async ({ native }) => {
  // The turn record of the native `turn.completed` event states the duration of the turn.
  await exerciseBasicChat(native, { timedDivider: true })
})
