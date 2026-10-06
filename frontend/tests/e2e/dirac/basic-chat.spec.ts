import { diracTest } from '../dirac-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

diracTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
