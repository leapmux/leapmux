import type { ServerInfo } from '../fixtures'
import type { WorkspaceFixture } from '../helpers/workspace'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { delimiter, join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { diracTest } from '../dirac-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { requireBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { createMockAgentEnvironment, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { withNativeWorker } from '../helpers/nativeWorker'
import { isAlive } from '../helpers/processTree'
import { createTestDirectory } from '../helpers/runDirectory'
import { loginViaToken, openWorkspace } from '../helpers/ui'
import { withTestWorkspace } from '../helpers/workspace'
import { readDiracMcpSessionObservation, writeDiracMcpWrapper } from './mcpConfiguration'

interface AnthropicDiracWorkspace extends WorkspaceFixture {
  server: ServerInfo
  agentId: string
  workingDir: string
}

/** Run Dirac's Anthropic settings against the suite mock through one private Worker. */
export const anthropicDiracTest = diracTest.extend<{
  anthropicModel: string
  anthropicDiracWorkspace: AnthropicDiracWorkspace
}>({
  anthropicModel: ['claude-haiku-4-5-20251001', { option: true }],
  anthropicDiracWorkspace: async ({ page, leapmuxServer, anthropicModel }, use) => {
    const privateHome = createTestDirectory('dirac-anthropic-home-')
    mkdirSync(join(privateHome, 'data'))
    writeFileSync(join(privateHome, 'data', 'globalState.json'), JSON.stringify({
      telemetrySetting: 'disabled',
      autoApproveAllToggled: true,
      yoloModeToggled: true,
    }), { mode: 0o600 })
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'dirac-anthropic-worker',
      workerName: 'Dirac Anthropic test',
      env: {
        DIRAC_DIR: privateHome,
        DIRAC_PROVIDER: 'anthropic',
        DIRAC_API_KEY: MODEL_KEY,
        DIRAC_BASE_URL: leapmuxServer.mockModelUrl,
        DIRAC_MODEL: anthropicModel,
        LEAPMUX_DIRAC_DEFAULT_MODEL: anthropicModel,
      },
      afterStop: (worker) => {
        if (worker.pid && isAlive(worker.pid))
          throw new Error('The private Dirac Worker did not exit.')
      },
    }, async ({ server }) => {
      await withTestWorkspace(server, 'dirac-anthropic', async (workspace) => {
        const workingDir = createTestDirectory('dirac-anthropic-wd-')
        const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspace.workspaceId, workingDir, {
          agentProvider: AgentProvider.DIRAC,
          model: anthropicModel,
          optionValues: { permissionMode: 'act', reasoning_effort: 'medium' },
        })
        await loginViaToken(page, server.adminToken)
        await openWorkspace(page, workspace.workspaceId)
        await use({ ...workspace, server, agentId, workingDir })
      })
    })
  },
})

export interface ConfiguredMcpDiracWorkspace extends WorkspaceFixture {
  server: ServerInfo
  agentId: string
  workingDir: string
  sessionReceipt: string
  formReceipt: string
  configuredServers: { name: string, command: string, args: string[], env: { name: string, value: string }[] }[]
}

/** Supply the native ACP server list through an isolated executable and private Worker. */
export const mcpDiracTest = diracTest.extend<{ configuredMcpDiracWorkspace: ConfiguredMcpDiracWorkspace }>({
  configuredMcpDiracWorkspace: async ({ page, leapmuxServer }, use) => {
    const runDirectory = createTestDirectory('dirac-mcp-private-')
    let workerAttempted = false
    let workerStopped = false
    await withCleanup(async () => {
      const environment = await createMockAgentEnvironment(runDirectory, leapmuxServer.mockModelUrl)
      const executable = requireBinary('dirac', 'The private Dirac MCP fixture requires its actual CLI', environment.env)
      const nodeExecutable = requireBinary('node', 'The private Dirac MCP fixture requires the Node executable', environment.env)
      const formReceipt = join(runDirectory, 'configured-form-receipt.json')
      const script = writeMcpFormServer(runDirectory, 'configured-form.mjs', { receiptLog: formReceipt })
      const configuredServers = [{ name: 'form_probe', command: nodeExecutable, args: [script], env: [] }]
      const wrapper = writeDiracMcpWrapper({ directory: join(runDirectory, 'wrapper'), executable, nodeExecutable, servers: configuredServers })
      workerAttempted = true
      await withNativeWorker({ ...leapmuxServer, agentEnv: environment.env }, {
        dataDirPrefix: 'dirac-mcp-worker',
        workerName: 'Dirac MCP test',
        env: { PATH: `${wrapper.directory}${delimiter}${environment.env.PATH}` },
        afterStop: (worker) => {
          if (worker.pid && isAlive(worker.pid))
            throw new Error('The private Dirac MCP Worker did not physically exit.')
          workerStopped = true
        },
      }, async ({ server }) => {
        await withTestWorkspace(server, 'dirac-mcp-private', async (workspace) => {
          const workingDir = createTestDirectory('dirac-mcp-native-wd-')
          const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspace.workspaceId, workingDir, agentOpenOptions(AgentProvider.DIRAC))
          await loginViaToken(page, server.adminToken)
          await openWorkspace(page, workspace.workspaceId)
          const active = await currentNativeAgent({ page, leapmuxServer: server })
          expect(active.id).toBe(agentId)
          const observed = readDiracMcpSessionObservation(wrapper.receiptLog)
          expect(observed.request.id).toBe(observed.reply.id)
          expect(observed.request.params).toEqual(expect.objectContaining({ cwd: workingDir, mcpServers: configuredServers }))
          expect(observed.sessionId).toBe(active.agentSessionId)
          await use({ ...workspace, server, workingDir, agentId, sessionReceipt: wrapper.receiptLog, formReceipt, configuredServers })
        })
      })
    }, async () => {
      if (!workerAttempted || workerStopped)
        rmSync(runDirectory, { recursive: true, force: true })
    })
  },
})
