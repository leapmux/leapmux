import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('runs successful and failed native commands with their actual output', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseShellToolExecution(context)
})
