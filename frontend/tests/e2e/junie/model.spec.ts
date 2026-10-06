import { JUNIE_RESPONSES_MODEL, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest.describe('Junie settings', () => {
  junieTest('a model switch reaches the native Responses endpoint and survives a reload', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${JUNIE_RESPONSES_MODEL}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${JUNIE_RESPONSES_MODEL}`)

    await modelScript.rule(
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Model switch task' } },
    )
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-model-answer', 'The selected model answered.')] })
    await sendMessage(page, modelScript.prompt('Reply once with the selected model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const selected = status.requests.find(request => request.stepIndex === 0)
    expect(selected?.path).toBe('/v1/responses')
    expect(selected?.body).toMatchObject({ model: MOCK_MODELS.junie })

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'model',
      value: JUNIE_RESPONSES_MODEL,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: MOCK_MODELS.junie })
        expect(request.path).toBe('/v1/responses')
      },
    })
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `model-${JUNIE_RESPONSES_MODEL}`)
  })
})
