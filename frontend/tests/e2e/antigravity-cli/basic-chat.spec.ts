import { antigravityTest } from '../antigravity-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

antigravityTest('completes a native conversation and preserves its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
