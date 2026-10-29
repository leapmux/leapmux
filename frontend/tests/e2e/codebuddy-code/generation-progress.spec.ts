import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { toolCallRow } from '../helpers/ui'

codebuddyTest('reports advancing tokens during native text output and preserves the completed answer', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})

codebuddyTest('reports advancing tokens during native reasoning and preserves the final answer', async ({ native }) => {
  await exerciseTokenProgress(native, {
    supported: true,
    step: { reasoning: 'The native reasoning stream supplies enough characters for two held chunks before the completed answer.' },
  })
})

codebuddyTest('reports no byte count throughout an actual native shell output stream', async ({ native }) => {
  await exerciseOutputByteProgress(native, {
    supported: false,
    unsupportedCounterScope: 'selected',
    prepareCompletedResultView: callId => expandNativeResultView(toolCallRow(native.page, callId)),
  })
})
