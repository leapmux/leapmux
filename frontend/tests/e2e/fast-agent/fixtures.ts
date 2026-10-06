import type { ServerInfo } from '../fixtures'
import type { WorkspaceFixture } from '../helpers/workspace'
import { copyFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fastAgentTest } from '../fastagent-fixtures'
import { FAST_AGENT_MOCK_MODEL } from '../helpers/mockAgentEnvironment'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { loginViaToken, openWorkspace } from '../helpers/ui'
import { withAgentWorkspace } from '../helpers/workspace'
import { FAST_AGENT_AGENT } from './scenarios'

interface FastAgentModesWorkspace extends WorkspaceFixture {
  server: ServerInfo
}

/** Load two private native agent cards without changing the suite's Fast Agent home. */
export const fastAgentModesTest = fastAgentTest.extend<{ fastAgentModesWorkspace: FastAgentModesWorkspace }>({
  fastAgentModesWorkspace: async ({ page, leapmuxServer }, use) => {
    const sharedHome = leapmuxServer.agentEnv.FAST_AGENT_HOME
    if (!sharedHome)
      throw new Error('The private Fast Agent fixture requires its suite mock configuration.')
    const home = createTestDirectory('fast-agent-modes-home-')
    copyFileSync(join(sharedHome, 'fast-agent.yaml'), join(home, 'fast-agent.yaml'))
    const cards = join(home, 'agent-cards')
    mkdirSync(cards)
    for (const [name, marker] of [['reader', 'NATIVE_FAST_AGENT_READER'], ['writer', 'NATIVE_FAST_AGENT_WRITER']]) {
      writeFileSync(join(cards, `${name}.md`), `---\nname: ${name}\ndescription: ${name} mode for a private E2E test.\nmodel: ${FAST_AGENT_MOCK_MODEL}\n${name === 'reader' ? 'default: true\n' : ''}---\n\nUse ${marker} as your private native instruction marker.\n`, { mode: 0o600 })
    }
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'fast-agent-modes-worker',
      workerName: 'Fast Agent modes',
      env: { FAST_AGENT_HOME: home },
    }, async ({ server }) => {
      await withAgentWorkspace(server, {
        ...FAST_AGENT_AGENT,
        prefix: 'fast-agent-modes',
        openOptions: { optionValues: { permissionMode: 'reader' } },
      }, async (workspace) => {
        await loginViaToken(page, server.adminToken)
        await openWorkspace(page, workspace.workspaceId)
        await use({ ...workspace, server })
      })
    })
  },
})
