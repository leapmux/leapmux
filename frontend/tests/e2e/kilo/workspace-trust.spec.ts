import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit, projectConfigurationWorker } from '../helpers/nativeWorkspaceTrustLimit'
import { createGitRepo } from '../helpers/worktree'
import { kiloTest } from '../kilo-fixtures'

kiloTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.KILO }
  await exerciseNativeWorkspaceTrustLimit(context, {
    worker: projectConfigurationWorker(leapmuxServer.agentEnv, { binaryName: 'kilo', holdWhen: ['acp'] }, 'KILO_DISABLE_PROJECT_CONFIG'),
    projectConfiguration: {
      prepare({ directory, marker }) {
        // Kilo reads AGENTS.md from the working directory up to the root of its git
        // repository. A repository of its own stops that search here, so the request
        // does not also carry the instructions of the LeapMux checkout around it.
        createGitRepo(directory, '.')
        writeFileSync(join(directory, 'AGENTS.md'), `# Native project configuration\nKeep ${marker} as a standing project instruction.\n`)
      },
      async prove(privateContext, { directory, marker }) {
        const request = await sendNativeAnswer(privateContext, 'Reply once after native project configuration loads.', 'The native project configuration turn completed.')
        const instructions = nativeModelInstructionText(request)
        expect(instructions).toContain(marker)
        expect(instructions).toContain(`Instructions from: ${join(directory, 'AGENTS.md')}`)
      },
    },
  })
})
