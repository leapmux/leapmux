import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset, expectSettingsChip, openPlusMenu, waitForSettingsHydrated } from '../helpers/ui'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest('smart-permissions-shortcut: offers only the bypass permission shortcut', async ({ authenticatedZCodeWorkspace, page }) => {
  void authenticatedZCodeWorkspace
  await waitForSettingsHydrated(page)
  const menu = await openPlusMenu(page)
  // ZCode declares no Smart preset, so only the bypass shortcut is drawn.
  await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Yolo')
})

zcodeTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  const relatedProof = () => exerciseRelatedTodo(context, { toolCall: updateTodosToolCall(AgentProvider.ZCODE, 'related-native-todo', [{ step: 'Native capability proof', status: 'pending' }]), item: 'Native capability proof', prepare: () => applyPermissionPreset(page, 'bypass') })
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof })
})
