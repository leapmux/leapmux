import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { cursorRequestContextToolCall } from '../helpers/providerToolCalls'
import { nativeContext } from './scenarios'

cursorTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare({ directory, marker }) {
        writeFileSync(join(directory, 'AGENTS.md'), `# Native project configuration\nKeep ${marker} as a standing project instruction.\n`)
      },
      async prove(privateContext, { directory, marker }) {
        // The Run request holds only the rules that a user attached to the message.
        // The CLI states the rules that it loaded from the project in its answer to the
        // request context query, which the scripted call makes before the turn ends.
        const request = await sendNativeAnswer(privateContext, 'Reply once after native project configuration loads.', 'The native project configuration turn completed.', { toolCalls: [cursorRequestContextToolCall('native-request-context')] })
        expect(request.nativeRequest?.contextRules).toEqual(expect.arrayContaining([expect.objectContaining({ path: join(directory, 'AGENTS.md'), content: expect.stringContaining(marker) })]))
      },
    },
  })
})
