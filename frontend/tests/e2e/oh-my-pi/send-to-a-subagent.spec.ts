import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { HELD_CHILD_NAME, HELD_CHILD_REPORT, HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('proves the unsupported native child send route while its original task runs', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await applyPermissionPreset(page, 'bypass')
  await expectUnsupportedSubagent(context, { operation: 'send', openChild: async () => {
    const child = await openHeldChildTab(page, modelScript, { provider: context.provider, rowTitle: HELD_CHILD_NAME, childTurn: { user: HELD_CHILD_TASK }, heldAnswer: { toolCalls: [ohMyPiYieldToolCall('held-child-yield', HELD_CHILD_REPORT)] }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] })
    return { row: child.row, childId: child.childTabId, parentId: child.rootTabId, finish: child.finish }
  } })
})
