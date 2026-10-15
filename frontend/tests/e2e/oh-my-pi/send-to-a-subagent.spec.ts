// The native steer_subagent command is not integrated.
// See https://github.com/leapmux/leapmux/issues/506.
import { openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiHeldChild } from './childScenario'

ohMyPiTest('proves the unsupported native child send route while its original task runs', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openHeldChildTab(native, ohMyPiHeldChild()) })
})
