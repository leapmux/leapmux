import { diracTest } from '../dirac-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { nativeContext } from './scenarios'

diracTest('shows the native model error and runs a later valid turn', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseModelError(context)
})
