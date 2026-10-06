import { exerciseBasicChat } from '../helpers/nativeConversation'
import { qoderTest } from '../qoder-fixtures'

qoderTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
