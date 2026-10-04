import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('closes the native provider and its owned tool process', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseCloseAgent(context)
})
