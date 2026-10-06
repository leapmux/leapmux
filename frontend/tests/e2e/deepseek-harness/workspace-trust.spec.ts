import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { deepseekHarnessModelContextText } from './modelContextText'
import { nativeContext } from './scenarios'

deepseekHarnessTest('loads actual workspace instructions without a native workspace trust question', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: project => writeFileSync(join(project.directory, 'AGENTS.md'), `${project.marker}: preserve the exact workspace instruction.\n`),
      prove: async (privateContext, project) => {
        const request = await sendNativeAnswer(privateContext, 'Read the actual workspace instruction.', 'The native workspace instruction reached the model.')
        expect(deepseekHarnessModelContextText(request)).toContain(project.marker)
      },
    },
  })
})
