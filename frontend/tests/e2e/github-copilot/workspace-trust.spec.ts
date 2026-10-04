import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare({ directory, marker }) {
        writeFileSync(join(directory, 'AGENTS.md'), `# Native project configuration\nKeep ${marker} as a standing project instruction.\n`)
      },
      async prove(privateContext, { marker }) {
        const request = await sendNativeAnswer(privateContext, 'Reply once after native project configuration loads.', 'The native project configuration turn completed.')
        expect(nativeModelInstructionText(request)).toContain(marker)
      },
    },
  })
})
