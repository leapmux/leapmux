import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('proves the unsupported native child send route while its original task runs', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await applyPermissionPreset(page, 'bypass')
  await expectUnsupportedSubagent(context, { operation: 'send', openChild: async () => {
    const child = await openHeldChildTab(page, modelScript, { provider: context.provider, childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] })
    return { row: child.row, childId: child.childTabId, parentId: child.rootTabId, finish: child.finish }
  } })
})
