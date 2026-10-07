import { exerciseBasicChat } from '../helpers/nativeConversation'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  // The native `turn.completed` event states the duration of the turn.
  await exerciseBasicChat(native, { timedDivider: true })
})
