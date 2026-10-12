import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

codebuddyTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  // The native `result` row states the duration of the turn.
  await exerciseBasicChat(native, { timedDivider: true })
})
