import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { HELD_CHILD_NAME, HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'

codewhaleTest('proves the unsupported native child interrupt route while its original task runs', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await applyPermissionPreset(page, 'bypass')
  await expectUnsupportedSubagent(context, { operation: 'interrupt', openChild: async () => {
    const child = await openHeldChildTab(page, modelScript, { provider: context.provider, rowTitle: HELD_CHILD_NAME, childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] })
    return { row: child.row, childId: child.childTabId, parentId: child.rootTabId, finish: child.finish }
  } })
})
