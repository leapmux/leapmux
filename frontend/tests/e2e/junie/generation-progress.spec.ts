import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

// Junie sends no ACP frame while the model streams its answer: it holds the whole response and states the
// answer text once, with the end of the turn. No live token count exists to report.
junieTest('exposes no token or byte counter throughout the completed native stream', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: false, counter: 'tokens', approveTool: false })
})

junieTest('reports an advancing byte count while native output arrives', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  // Junie asks to approve the controlled shell command, and then streams its output while it runs.
  await exerciseGenerationProgress(context, { supported: true, counter: 'bytes', approveTool: true })
})
