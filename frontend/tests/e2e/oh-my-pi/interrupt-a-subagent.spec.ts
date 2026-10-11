import { openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiHeldChild } from './childScenario'

ohMyPiTest('proves the unsupported native child interrupt route while its original task runs', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openHeldChildTab(native, ohMyPiHeldChild()) })
})
