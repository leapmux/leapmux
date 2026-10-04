import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare({ directory, marker }) {
        writeFileSync(join(directory, 'AGENTS.md'), `# Native project configuration\nKeep ${marker} as a standing project instruction.\n`)
      },
      async prove(privateContext, { directory, marker }) {
        const request = await sendNativeAnswer(privateContext, 'Reply once after native project configuration loads.', 'The native project configuration turn completed.')
        expect(request.nativeRequest?.cursorRules).toEqual(expect.arrayContaining([expect.objectContaining({ path: join(directory, 'AGENTS.md'), content: expect.stringContaining(marker) })]))
      },
    },
  })
})
