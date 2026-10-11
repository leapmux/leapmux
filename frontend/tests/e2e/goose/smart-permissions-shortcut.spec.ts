import { gooseTest } from '../goose-fixtures'
import { exerciseGoosePermissionRemoval, exerciseGooseShortcutSwitch } from './permissionScenario'

gooseTest('permission shortcuts switch Smart Approve and Auto', async ({ native }) => {
  await exerciseGooseShortcutSwitch(native.page)
})

gooseTest('asks before a real removal after the Smart shortcut and executes it through Auto', async ({ native }) => {
  await exerciseGoosePermissionRemoval(native)
})
