import { commandCodeTest } from '../command-code-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { nativeContext } from './scenarios'

commandCodeTest('shows the native model failure and accepts a later valid turn', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await exerciseModelError(context)
})
