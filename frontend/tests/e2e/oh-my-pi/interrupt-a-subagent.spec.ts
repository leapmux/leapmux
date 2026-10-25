import { exerciseChildInterrupt } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiHeldChild } from './childScenario'

ohMyPiTest('interrupts the native child without stopping its parent', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await exerciseChildInterrupt(native, { ...ohMyPiHeldChild(), finalStatus: 'stopped' })
})
