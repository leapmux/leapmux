import { ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { HELD_CHILD_NAME, HELD_CHILD_REPORT, HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('proves the unsupported native child send route while its original task runs', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openHeldChildTab(native, { rowTitle: HELD_CHILD_NAME, heldAnswer: { toolCalls: [ohMyPiYieldToolCall('held-child-yield', HELD_CHILD_REPORT)] }, childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] }) })
})
