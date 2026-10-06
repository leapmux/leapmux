import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { toolCallRow } from '../helpers/ui'

codebuddyTest('exposes no token or byte counter throughout the completed native stream', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: false })
})

codebuddyTest('reports no byte count throughout an actual native shell output stream', async ({ native }) => {
  await exerciseOutputByteProgress(native, {
    supported: false,
    prepareCompletedResultView: callId => expandNativeResultView(toolCallRow(native.page, callId)),
  })
})
