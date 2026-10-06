import { openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { mimoTest } from '../mimo-fixtures'
import { MIMO_HELD_CHILD_TURN } from './childScenario'

mimoTest('proves the unsupported native child interrupt route while its original task runs', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openHeldChildTab(native, { childTurn: MIMO_HELD_CHILD_TURN, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] }) })
})
