import { diracTest } from '../dirac-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'

diracTest('runs successful and failed native commands with their actual output', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseShellToolExecution(context)
})
