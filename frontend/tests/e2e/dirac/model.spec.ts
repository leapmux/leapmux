import { diracTest, expect } from '../dirac-fixtures'
import { exerciseNativeOption } from '../helpers/nativeSettings'

diracTest.describe('Dirac model and steering', () => {
  diracTest('sends a selected model on the next request and keeps it after reload', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: 'gpt-6-astra',
      nativeProof: (request) => {
        expect(request.protocol).toBe('openai-chat-completions')
        expect(request.body).toMatchObject({ model: 'gpt-6-astra' })
      },
    })
  })
})
