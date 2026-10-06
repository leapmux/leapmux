import { KILO_AGENT, kiloTest } from '../kilo-fixtures'
import { exerciseOpenCodeFamilyCodeExecution } from '../opencode/codeExecution'
import { nativeContext } from './scenarios'

kiloTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseOpenCodeFamilyCodeExecution(context, { providerAgent: KILO_AGENT, configurationVariable: 'KILO_CONFIG_CONTENT', codeModeVariable: 'KILO_EXPERIMENTAL_CODE_MODE' })
})
