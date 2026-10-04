import { commandCodeTest } from '../command-code-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

commandCodeTest('stops the actual provider and its owned command processes', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await exerciseCloseAgent(context)
})
