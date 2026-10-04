import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('refuses mid-turn steering and runs the queued prompt in a separate native turn', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseQueuedTurnWithoutSteering(context)
})
