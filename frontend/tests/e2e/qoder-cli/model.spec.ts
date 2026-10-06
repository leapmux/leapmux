import { MOCK_MODELS, QODER_ALTERNATE_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI settings', () => {
  qoderTest('switches the model used by the next request', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: QODER_ALTERNATE_MODEL_ID,
      nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.qoder }),
    })
  })
})
