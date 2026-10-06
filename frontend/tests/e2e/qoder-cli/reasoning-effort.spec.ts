import { MOCK_MODELS, QODER_ALTERNATE_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption, exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI effort and session goal', () => {
  qoderTest('uses the selected reasoning effort in the next native model request', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page)
    await modelScript.queue({ text: 'PRIOR_QODER_EFFORT_CONTEXT' })
    await sendMessage(page, modelScript.prompt('Remember the effort context marker.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expect(page.locator('[data-testid="composer-effort-trigger"]:visible')).toContainText('Low')

    await modelScript.queue({ text: 'LOW_EFFORT_APPLIED' })
    await sendMessage(page, modelScript.prompt('Reply once using the selected effort.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = status.requests.find(item => item.stepIndex === 1)
    expect(request).toBeDefined()
    const body = JSON.stringify(request?.body)
    expect(request?.body).toMatchObject({ reasoning_effort: 'low' })
    expect(body.includes('PRIOR_QODER_EFFORT_CONTEXT')).toBe(true)

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'effort',
      value: 'low',
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ reasoning_effort: 'low' })
      },
    })
    await expect(page.locator('[data-testid="composer-effort-trigger"]:visible')).toContainText('Low')
  })
})

qoderTest.describe('Qoder CLI model switch', () => {
  // The effort is a launch flag that does not depend on the model, so a model switch must keep it on screen
  // and in the Worker row, and it must restart nothing. The alternate mock model declares no reasoning, so
  // Qoder sends no effort for it. The native proof is the model, and the kept setting comes from the helper.
  // The native session takes the flag after its first turn, as the effort test above does.
  qoderTest('keeps the chosen effort after a model switch and a reload', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
    await exerciseModelSwitchKeepsOption(context, {
      prepare: async () => {
        await sendNativeAnswer(context, 'Reply once before the effort changes.', 'The first turn answered.')
      },
      kept: { groupId: 'effort', value: 'low' },
      model: QODER_ALTERNATE_MODEL_ID,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: MOCK_MODELS.qoder })
      },
    })
  })
})
