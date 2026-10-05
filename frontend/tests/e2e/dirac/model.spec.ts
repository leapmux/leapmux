import { expect as diracExpect, diracTest, openDiracAgent } from '../dirac-fixtures'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

diracTest.describe('Dirac model and steering', () => {
  diracTest('sends a selected model on the next request and keeps it after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'model-gpt-6-astra')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'model-gpt-6-astra')

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-model-answer', 'complete', 'The selected model answered.')] })
    await sendMessage(page, modelScript.prompt('Reply once with the selected model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = status.requests.find(record => record.stepIndex === 0)
    diracExpect(request?.protocol).toBe('openai-chat-completions')
    diracExpect(request?.body).toMatchObject({ model: 'gpt-6-astra' })

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'model',
      value: 'gpt-6-astra',
      nativeProof: (request) => {
        diracExpect(request.body).toMatchObject({ model: 'gpt-6-astra' })
        diracExpect(request.protocol).toBe('openai-chat-completions')
      },
    })
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, 'model-gpt-6-astra')
  })
})
