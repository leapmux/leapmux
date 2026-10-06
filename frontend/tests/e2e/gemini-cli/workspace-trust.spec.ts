import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { nativeContext } from './scenarios'

geminiTest('reads native workspace instructions without a workspace trust control', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: project => writeFileSync(join(project.directory, 'GEMINI.md'), `${project.marker}: preserve the workspace instruction.\n`),
      prove: async (privateContext, project) => {
        const request = await sendNativeAnswer(privateContext, 'Read the native workspace instruction.', 'The native workspace instruction reached the mock.')
        expect(nativeModelContextText(request)).toContain(project.marker)
      },
    },
  })
})
