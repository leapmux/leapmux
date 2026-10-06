import type { ServerInfo } from '../fixtures'
import type { PrivateWorkerSetup } from '../helpers/privateNativeWorkspace'
import type { WorkspaceFixture } from '../helpers/workspace'
import { copyFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fastAgentTest } from '../fastagent-fixtures'
import { FAST_AGENT_MOCK_MODEL } from '../helpers/mockAgentEnvironment'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { openProviderAgent } from '../helpers/workspace'
import { FAST_AGENT_AGENT } from './scenarios'

interface FastAgentModesWorkspace extends WorkspaceFixture {
  server: ServerInfo
}

/**
 * Write a private Fast Agent home into `runDirectory`: the suite's mock configuration from `sharedHome`, and two native
 * agent cards, `reader` (the default) and `writer`, each with its own instruction marker.
 */
export function prepareFastAgentModes(runDirectory: string, sharedHome: string): PrivateWorkerSetup<undefined> {
  const home = join(runDirectory, 'fast-agent-home')
  const cards = join(home, 'agent-cards')
  mkdirSync(cards, { recursive: true })
  copyFileSync(join(sharedHome, 'fast-agent.yaml'), join(home, 'fast-agent.yaml'))
  for (const [name, marker] of [['reader', 'NATIVE_FAST_AGENT_READER'], ['writer', 'NATIVE_FAST_AGENT_WRITER']]) {
    writeFileSync(join(cards, `${name}.md`), `---\nname: ${name}\ndescription: ${name} mode for a private E2E test.\nmodel: ${FAST_AGENT_MOCK_MODEL}\n${name === 'reader' ? 'default: true\n' : ''}---\n\nUse ${marker} as your private native instruction marker.\n`, { mode: 0o600 })
  }
  return { env: { FAST_AGENT_HOME: home }, setup: undefined }
}

/** Load two private native agent cards without changing the suite's Fast Agent home. */
export const fastAgentModesTest = fastAgentTest.extend<{ fastAgentModesWorkspace: FastAgentModesWorkspace }>({
  fastAgentModesWorkspace: async ({ page, leapmuxServer }, use) => {
    const sharedHome = leapmuxServer.agentEnv.FAST_AGENT_HOME
    if (!sharedHome)
      throw new Error('The private Fast Agent fixture requires its suite mock configuration.')
    await withPrivateNativeWorkspace(page, leapmuxServer, {
      prefix: 'fast-agent-modes',
      workerName: 'Fast Agent modes',
      providerAgent: FAST_AGENT_AGENT,
      prepare: runDirectory => prepareFastAgentModes(runDirectory, sharedHome),
      openAgent: async (server, workspaceId, workingDir) =>
        (await openProviderAgent(server, workspaceId, FAST_AGENT_AGENT, { workingDir, optionValues: { permissionMode: 'reader' } })).agentId,
    }, async ({ workspaceId, server, workingDir }) => {
      await use({ workspaceId, server, workingDir })
    })
  },
})
