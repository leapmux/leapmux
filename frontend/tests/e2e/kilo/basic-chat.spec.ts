import { exerciseBasicChat } from '../helpers/nativeConversation'
import { kiloTest } from '../kilo-fixtures'

kiloTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
