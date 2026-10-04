import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { kiroToolResult } from '../helpers/kiroToolResult'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('proves the absent shortcut after a native shell operation', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  context.readToolResult = kiroToolResult
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
