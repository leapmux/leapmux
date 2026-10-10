import { exerciseBasicChat } from '../helpers/nativeConversation'
import { junieTest } from '../junie-fixtures'

junieTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  // Junie states the prompt in its `## ISSUE DESCRIPTION` row and sends its capability and
  // project listings as its own user rows around it. The turn reader classifies those rows
  // as context, so the prompt check needs no override.
  await exerciseBasicChat(native)
})
