import { diracTest } from '../dirac-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { uniqueMarker } from '../helpers/shellArguments'
import { nativeContext } from './scenarios'

diracTest('reports an advancing token count while native output arrives', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', approveTool: false })
})

// Dirac 0.5.17 runs `execute_command` in a process of its own: its CLI forces
// `vscodeTerminalExecutionMode: "backgroundExec"`, so no ACP terminal carries the
// output, and the call states its output only in the update that follows the exit.
diracTest('reports no byte count throughout an actual native shell output stream', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  const markers = {
    first: uniqueMarker('NATIVEFIRST'),
    second: uniqueMarker('NATIVESECOND'),
  }
  await exerciseGenerationProgress(context, {
    supported: false,
    counter: 'bytes',
    approveTool: false,
    outputMarkers: markers,
    // Dirac gives the call an ID of its own, so the first marker identifies the
    // result. The collapsed result shows its first three lines, which hold the
    // first marker and not the second.
    prepareCompletedResultView: async () => {
      const result = page.locator('[data-testid="message-bubble"][data-tool-row-role="result"]:visible').filter({ hasText: markers.first })
      await expandNativeResultView(result)
    },
  })
})
