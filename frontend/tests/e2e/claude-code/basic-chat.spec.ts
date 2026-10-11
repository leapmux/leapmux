import { claudeTest } from '../claude-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

claudeTest('ends an actual native chat turn and restores its answer after reload', async ({ native }) => {
  // The native `result` row states the duration of the turn.
  await exerciseBasicChat(native, { timedDivider: true })
})
