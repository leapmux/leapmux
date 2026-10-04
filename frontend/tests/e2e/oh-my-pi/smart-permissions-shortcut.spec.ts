import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('proves the absent shortcut after a native shell operation', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
