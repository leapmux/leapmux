import { exerciseBasicChat } from '../helpers/nativeConversation'
import { museTest } from '../muse-fixtures'

museTest('ends the native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native, { timedDivider: true })
})
