import { expect } from '@playwright/test'
import { OH_MY_PI_ALT_MODEL_ID, OH_MY_PI_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The selected model must reach an actual native request. The setting must survive a page reload.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest('switches the model for the next native request', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'model',
    value: OH_MY_PI_ALT_MODEL_ID,
    nativeProof: request => expect(request.body).toHaveProperty('model', OH_MY_PI_ALT_MODEL_WIRE_ID),
  })
  await expectSettingsChip(native.page, 'GLM-5.3 Alternate')
})
