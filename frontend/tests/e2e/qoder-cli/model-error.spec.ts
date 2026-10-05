import { exerciseModelError, nativeErrorMarker } from '../helpers/nativeModelError'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('shows the native model error and runs a later valid turn', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await exerciseModelError(context, {
    // qodercli 1.1.65 replaces the message of an HTTP status error with its own
    // text ("The request could not be completed. Please try again."), so that
    // message never reaches the transcript. Qoder relays the provider's own
    // words only for a stream that fails after a partial answer, in the
    // `errors` of its `result`.
    error: { status: 500, code: 'stream_error', message: nativeErrorMarker(), midStream: true },
  })
})
