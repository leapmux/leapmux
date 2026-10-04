import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('reports an advancing token count while native output arrives', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', approveTool: false })
})

junieTest('reports an advancing byte count while native output arrives', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'bytes', approveTool: false })
})
