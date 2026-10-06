import { gooseTest } from '../goose-fixtures'
import { exerciseGoosePermissionRemoval, exerciseGooseShortcutSwitch } from './permissionScenario'

gooseTest('bypass-permissions-shortcut: permission shortcuts switch Smart Approve and Auto', async ({ native }) => {
  await exerciseGooseShortcutSwitch(native.page)
})

gooseTest('bypass-permissions-shortcut: smart mode asks before a removal and auto mode runs it', async ({ native }) => {
  await exerciseGoosePermissionRemoval(native)
})
