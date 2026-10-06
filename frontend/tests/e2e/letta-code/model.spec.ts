import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, closeComposerMenus, expectSettingsChip, expectSettingsOptionChosen, openPlusMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code settings', () => {
  lettaTest('offers the configured model and the permission modes', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, MOCK_MODELS.letta)

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)
  })

  lettaTest('sends a selected model on the next request and keeps it after reload', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedLettaWorkspace
    const alternate = `openai/${MOCK_MODELS.openai}`
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${alternate}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${alternate}`)

    await modelScript.queue({ text: 'The selected model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once after the model switch.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ model: MOCK_MODELS.openai })

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'model',
      value: alternate,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: MOCK_MODELS.openai })
      },
    })
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `model-${alternate}`)
  })
})
