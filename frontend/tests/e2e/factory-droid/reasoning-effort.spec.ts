import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { droidNativeSettingsUpdates } from '../helpers/droidNativeSettings'
import { DROID_MOCK_MODEL_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

droidTest.describe('Factory Droid settings', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('sends a built-in model effort to the isolated mock', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'model-claude-fable-5.1')
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)

    await expect.poll(async () => (await droidNativeSettingsUpdates(leapmuxServer, authenticatedDroidWorkspace.workspaceId)).some(update =>
      update.requestId?.startsWith('leapmux-') && update.modelId === 'claude-fable-5.1' && update.reasoningEffort === 'high')).toBe(true)
    await expectSettingsOptionChosen(page, 'effort-high')

    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ text: 'Droid answered at high effort.' })
    await sendMessage(page, modelScript.prompt('Reply once after the effort switch.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = status.requests.find(record => record.stepIndex === 0)
    expect(request?.path).toBe('/v1/api/llm/a/v1/messages')
    expect(request?.body).toMatchObject({ model: 'claude-fable-5.1', output_config: { effort: 'high' } })

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'effort',
      value: 'high',
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: 'claude-fable-5.1', output_config: { effort: 'high' } })
        expect(request.path).toBe('/v1/api/llm/a/v1/messages')
      },
    })

    await chooseSettingsOption(page, `model-${DROID_MOCK_MODEL_IDS.primary}`)
    await waitForSettingsIdle(page)
  })
})
