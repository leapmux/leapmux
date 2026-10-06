import { codewhaleTest } from '../codewhale-fixtures'
import { HELD_CHILD_NAME, HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'

codewhaleTest('proves the unsupported native child interrupt route while its original task runs', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openHeldChildTab(native, { rowTitle: HELD_CHILD_NAME, childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] }) })
})
