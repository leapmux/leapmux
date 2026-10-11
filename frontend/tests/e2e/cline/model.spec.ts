import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { closeComposerMenus, expectSettingsChip, offeredSettingsOptions, openPlusMenu, settingsGroupTrigger, waitForSettingsHydrated } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * The selected model must reach an actual native request. The setting must survive a page reload.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * Cline's hub exposes no model catalog. The Worker combines its native provider catalog with the configured custom model.
 */
clineTest.describe('Cline settings', () => {
  clineTest('switches to a model from the native provider catalog', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await exerciseNativeOption(context, {
      groupId: 'model',
      value: 'deepseek-v4-pro',
      nativeProof: request => expect(request.body).toHaveProperty('model', 'deepseek-v4-pro'),
    })
    await expectSettingsChip(page, 'DeepSeek V4 Pro')
  })

  clineTest('offers the configured model, the three modes, and no effort for a model without a ladder', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, MOCK_MODELS.cline)
    await expectSettingsChip(page, 'Act')
    expect(await offeredSettingsOptions(page, 'permissionMode')).toEqual(['plan', 'act', 'auto_approve'])

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toHaveCount(0)
    await closeComposerMenus(page)

    const request = await sendNativeAnswer(context, 'Reply once with the configured model.', 'The configured model answered.')
    expect(request.body).toHaveProperty('model', MOCK_MODELS.cline)
  })
})
