import { OPENCODE_AGENT, opencodeTest } from '../opencode-fixtures'
import { exerciseOpenCodeFamilyCodeExecution } from './codeExecution'
import { nativeContext } from './scenarios'

opencodeTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseOpenCodeFamilyCodeExecution(context, { providerAgent: OPENCODE_AGENT, configurationVariable: 'OPENCODE_CONFIG_CONTENT', codeModeVariable: 'OPENCODE_EXPERIMENTAL_CODE_MODE' })
})
