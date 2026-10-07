import { exerciseBasicChat } from '../helpers/nativeConversation'
import { junieTest } from '../junie-fixtures'

junieTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  // Junie states the prompt in its `## ISSUE DESCRIPTION` row, and sends its own listing of the working directory as a
  // user row after it.
  await exerciseBasicChat(native, { nativeRowsAfterPrompt: /## PROJECT STRUCTURE\n[\s\S]*/ })
})
