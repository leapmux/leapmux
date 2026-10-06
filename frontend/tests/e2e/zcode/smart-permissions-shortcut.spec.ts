import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset, expectPermissionShortcuts, expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('smart-permissions-shortcut: offers only the bypass permission shortcut', async ({ authenticatedZCodeWorkspace, page }) => {
  void authenticatedZCodeWorkspace
  await waitForSettingsHydrated(page)
  // ZCode declares no Smart preset, so only the bypass shortcut is drawn.
  await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Yolo')
})

zcodeTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  const relatedProof = () => exerciseRelatedTodo(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof })
})
