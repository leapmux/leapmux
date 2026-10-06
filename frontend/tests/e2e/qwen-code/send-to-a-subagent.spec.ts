import { openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { qwenTest } from '../qwen-fixtures'
import { QWEN_HELD_CHILD_TURN } from './childScenario'

qwenTest('proves the unsupported native child send route while its original task runs', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openHeldChildTab(native, { childTurn: QWEN_HELD_CHILD_TURN, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] }) })
})
