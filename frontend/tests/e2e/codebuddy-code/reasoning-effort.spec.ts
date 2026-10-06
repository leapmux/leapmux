import { CODEBUDDY_EFFORT_LEVEL, CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { CODEBUDDY_ALT_MODEL_ID, CODEBUDDY_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, closeComposerMenus, expectSettingsChip, expectSettingsOptionChosen, openPlusMenu, openSettingsMenu, settingsGroupTrigger, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('switches the effort and the mode, and keeps them after a reload', async ({ authenticatedCodebuddyWorkspace, page }) => {
    void authenticatedCodebuddyWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Bypass Permissions')
    // The account and local model catalog determine the model choices.
    // This case checks that the model group appears.
    await openSettingsMenu(page, 'model')
    await closeComposerMenus(page)

    await chooseSettingsOption(page, `effort-${CODEBUDDY_EFFORT_LEVEL.Low}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')

    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.AcceptEdits}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Accept Edits')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Low')
    await expectSettingsChip(page, 'Accept Edits')
    await expectSettingsOptionChosen(page, `permissionMode-${CODEBUDDY_MODE.AcceptEdits}`)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)
  })

  codebuddyTest('sends a selected effort in the next native request', async ({ native }) => {
    const baseline = 'The first effort probe answered.'
    await exerciseNativeOption(native, {
      groupId: 'effort',
      value: CODEBUDDY_EFFORT_LEVEL.Low,
      // The native session takes the effort after its first turn.
      prepare: async () => {
        await sendNativeAnswer(native, 'Reply before I change effort.', baseline)
      },
      nativeProof: (request) => {
        expect(request.protocol).toBe('openai-chat-completions')
        expect(request.body).toMatchObject({ reasoning_effort: 'low' })
        expect(JSON.stringify(request.body)).toContain(baseline)
      },
    })
    await expectSettingsChip(native.page, 'Low')
  })
})

codebuddyTest.describe('CodeBuddy Code model switch', () => {
  // The effort is a launch argument that does not depend on the model, so a model switch must keep it on
  // screen and in the Worker row, and it must restart nothing. The alternate mock model declares no
  // reasoning, so CodeBuddy sends no effort for it. The native proof is the model, and the kept setting
  // comes from the helper. The native session takes the argument after its first turn, as the test above does.
  codebuddyTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
    await exerciseModelSwitchKeepsOption(native, {
      prepare: async () => {
        await sendNativeAnswer(native, 'Reply once before the effort changes.', 'The first turn answered.')
      },
      kept: { groupId: 'effort', value: CODEBUDDY_EFFORT_LEVEL.Low },
      model: CODEBUDDY_ALT_MODEL_ID,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: CODEBUDDY_ALT_MODEL_WIRE_ID })
      },
    })
  })
})
