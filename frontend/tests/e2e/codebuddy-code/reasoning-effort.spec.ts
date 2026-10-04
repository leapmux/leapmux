import { CODEBUDDY_EFFORT_LEVEL, CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from '../codebuddy-fixtures'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, closeComposerMenus, expectSettingsChip, openPlusMenu, openSettingsMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('switches the effort and the mode, and keeps them after a reload', async ({ codebuddyWorkspace, page }) => {
    void codebuddyWorkspace
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
    const group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator(`[data-testid="permissionMode-${CODEBUDDY_MODE.AcceptEdits}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)
  })

  codebuddyTest('sends a selected effort in the next native request', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: 'The first effort probe answered.' })
    await sendMessage(page, modelScript.prompt('Reply before I change effort.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await chooseSettingsOption(page, `effort-${CODEBUDDY_EFFORT_LEVEL.Low}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')
    await modelScript.queue({ text: 'The low-effort probe answered.' })
    await sendMessage(page, modelScript.prompt('Reply after I change effort.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const next = status.requests.find(request => request.stepIndex === 1)
    expect(next?.protocol).toBe('openai-chat-completions')
    if (!next?.body || typeof next.body !== 'object' || !('reasoning_effort' in next.body))
      throw new Error('the CodeBuddy model request must state its selected effort')
    expect(next.body.reasoning_effort).toBe('low')
    expect(JSON.stringify(next.body).includes('The first effort probe answered.')).toBe(true)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'effort',
      value: CODEBUDDY_EFFORT_LEVEL.Low,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ reasoning_effort: 'low' })
      },
    })
  })
})
