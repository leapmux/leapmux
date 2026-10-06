import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { qwenTest } from '../qwen-fixtures'

qwenTest('proves the unsupported native child send route while its original task runs', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await applyPermissionPreset(page, 'bypass')
  await expectUnsupportedSubagent(context, { operation: 'send', openChild: async () => {
    const child = await openHeldChildTab(page, modelScript, { provider: context.provider, childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] })
    return { row: child.row, childId: child.childTabId, parentId: child.rootTabId, finish: child.finish }
  } })
})
