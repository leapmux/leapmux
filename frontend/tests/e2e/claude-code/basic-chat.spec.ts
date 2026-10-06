import { claudeTest } from '../claude-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

claudeTest('ends an actual native chat turn and restores its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
