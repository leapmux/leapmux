import { diracTest } from '../dirac-fixtures'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { uniqueMarker } from '../helpers/shellArguments'

diracTest('reports an advancing token count while native output arrives', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})

// Dirac 0.5.17 runs `execute_command` in a process of its own: its CLI forces
// `vscodeTerminalExecutionMode: "backgroundExec"`, so no ACP terminal carries the
// output, and the call states its output only in the update that follows the exit.
diracTest('reports no byte count throughout an actual native shell output stream', async ({ native }) => {
  const markers = {
    first: uniqueMarker('NATIVEFIRST'),
    second: uniqueMarker('NATIVESECOND'),
  }
  await exerciseOutputByteProgress(native, {
    supported: false,
    outputMarkers: markers,
    // Dirac gives the call an ID of its own, so the first marker identifies the
    // result. The collapsed result shows its first three lines, which hold the
    // first marker and not the second.
    prepareCompletedResultView: async () => {
      const result = native.page.locator('[data-testid="message-bubble"][data-tool-row-role="result"]:visible').filter({ hasText: markers.first })
      await expandNativeResultView(result)
    },
  })
})
