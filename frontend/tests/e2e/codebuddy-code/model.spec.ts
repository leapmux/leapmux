import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from '../codebuddy-fixtures'
import { CODEBUDDY_ALT_MODEL_ID, CODEBUDDY_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, closeComposerMenus, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('switches the model for the next native request', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${CODEBUDDY_ALT_MODEL_ID}`)
    await waitForSettingsIdle(page)

    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the alternate model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const body = status.requests.find(request => request.stepIndex === 0)?.body
    if (!body || typeof body !== 'object' || !('model' in body))
      throw new Error('the CodeBuddy model request must state its model')
    expect(body.model).toBe(CODEBUDDY_ALT_MODEL_WIRE_ID)

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'model',
      value: CODEBUDDY_ALT_MODEL_ID,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: CODEBUDDY_ALT_MODEL_WIRE_ID })
      },
    })
    const group = await openSettingsMenu(page, 'model')
    await expect(group.getByTestId(`model-${CODEBUDDY_ALT_MODEL_ID}`)).toHaveAttribute('aria-checked', 'true')
    await closeComposerMenus(page)
  })
})
