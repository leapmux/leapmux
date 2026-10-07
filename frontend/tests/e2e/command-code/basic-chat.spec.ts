import { commandCodeTest } from '../command-code-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

commandCodeTest('ends the native turn and keeps its answer after reload', async ({ native }) => {
  // The native `turn/completed` frame states no duration. The Worker measures the turn and adds the duration to the
  // turn end.
  await exerciseBasicChat(native, { timedDivider: true })
})
