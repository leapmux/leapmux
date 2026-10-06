import type { ServerInfo } from '../fixtures'
import type { PrivateWorkerSetup } from '../helpers/privateNativeWorkspace'
import type { WorkspaceFixture } from '../helpers/workspace'
import type { DiracStdioMcpServer } from './mcpConfiguration'
import { mkdirSync, writeFileSync } from 'node:fs'
import { delimiter, join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { diracTest } from '../dirac-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { requireBinary } from '../helpers/binaryOnPath'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { createMockAgentEnvironment, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { readDiracMcpSessionObservation, writeDiracMcpWrapper } from './mcpConfiguration'
import { DIRAC_AGENT } from './scenarios'

interface AnthropicDiracWorkspace extends WorkspaceFixture {
  server: ServerInfo
  agentId: string
  workingDir: string
}

/**
 * Write the private Dirac home of the Anthropic settings into `runDirectory`, and return the Worker environment that
 * points Dirac at the suite mock with `model`.
 */
export function prepareAnthropicDirac(runDirectory: string, mockModelUrl: string, model: string): PrivateWorkerSetup<undefined> {
  const privateHome = join(runDirectory, 'dirac-home')
  mkdirSync(join(privateHome, 'data'), { recursive: true })
  writeFileSync(join(privateHome, 'data', 'globalState.json'), JSON.stringify({
    telemetrySetting: 'disabled',
    autoApproveAllToggled: true,
    yoloModeToggled: true,
  }), { mode: 0o600 })
  return {
    env: {
      DIRAC_DIR: privateHome,
      DIRAC_PROVIDER: 'anthropic',
      DIRAC_API_KEY: MODEL_KEY,
      DIRAC_BASE_URL: mockModelUrl,
      DIRAC_MODEL: model,
      LEAPMUX_DIRAC_DEFAULT_MODEL: model,
    },
    setup: undefined,
  }
}

/** Run Dirac's Anthropic settings against the suite mock through one private Worker. */
export const anthropicDiracTest = diracTest.extend<{
  anthropicModel: string
  anthropicDiracWorkspace: AnthropicDiracWorkspace
}>({
  anthropicModel: ['claude-haiku-4-5-20251001', { option: true }],
  anthropicDiracWorkspace: async ({ page, leapmuxServer, anthropicModel }, use) => {
    await withPrivateNativeWorkspace(page, leapmuxServer, {
      prefix: 'dirac-anthropic',
      workerName: 'Dirac Anthropic test',
      providerAgent: DIRAC_AGENT,
      prepare: runDirectory => prepareAnthropicDirac(runDirectory, leapmuxServer.mockModelUrl, anthropicModel),
      openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
        agentProvider: AgentProvider.DIRAC,
        model: anthropicModel,
        optionValues: { permissionMode: 'act', reasoning_effort: 'medium' },
      }),
    }, async ({ workspaceId, server, agentId, workingDir }) => {
      await use({ workspaceId, server, agentId, workingDir })
    })
  },
})

export interface ConfiguredMcpDiracWorkspace extends WorkspaceFixture {
  server: ServerInfo
  agentId: string
  workingDir: string
  sessionReceipt: string
  formReceipt: string
  configuredServers: DiracStdioMcpServer[]
}

/** What the private files of the Dirac MCP Worker hold. */
interface DiracMcpFiles {
  sessionReceipt: string
  formReceipt: string
  configuredServers: DiracStdioMcpServer[]
}

/**
 * Write the private files of the Dirac MCP Worker into `runDirectory`: a mock agent environment, the form server, and
 * a wrapper executable first on PATH that hands the configured server list to Dirac's ACP session.
 */
export async function prepareMcpDirac(runDirectory: string, mockModelUrl: string): Promise<PrivateWorkerSetup<DiracMcpFiles>> {
  const environment = await createMockAgentEnvironment(runDirectory, mockModelUrl)
  const executable = requireBinary('dirac', 'The private Dirac MCP fixture requires its actual CLI', environment.env)
  const nodeExecutable = requireBinary('node', 'The private Dirac MCP fixture requires the Node executable', environment.env)
  const formReceipt = join(runDirectory, 'configured-form-receipt.json')
  const formServer = writeMcpFormServer(runDirectory, 'configured-form.mjs', { receiptLog: formReceipt })
  const configuredServers = [{ name: formServer.name, command: nodeExecutable, args: [...formServer.args], env: [] }]
  const wrapper = writeDiracMcpWrapper({ directory: join(runDirectory, 'wrapper'), executable, nodeExecutable, servers: configuredServers })
  return {
    agentEnv: environment.env,
    env: { PATH: `${wrapper.directory}${delimiter}${environment.env.PATH}` },
    setup: { sessionReceipt: wrapper.receiptLog, formReceipt, configuredServers },
  }
}

/** Supply the native ACP server list through an isolated executable and private Worker. */
export const mcpDiracTest = diracTest.extend<{ configuredMcpDiracWorkspace: ConfiguredMcpDiracWorkspace }>({
  configuredMcpDiracWorkspace: async ({ page, leapmuxServer }, use) => {
    await withPrivateNativeWorkspace(page, leapmuxServer, {
      prefix: 'dirac-mcp',
      workerName: 'Dirac MCP test',
      providerAgent: DIRAC_AGENT,
      prepare: runDirectory => prepareMcpDirac(runDirectory, leapmuxServer.mockModelUrl),
      openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, agentOpenOptions(AgentProvider.DIRAC)),
    }, async ({ workspaceId, server, agentId, workingDir, agent, setup }) => {
      const observed = readDiracMcpSessionObservation(setup.sessionReceipt)
      expect(observed.request.id).toBe(observed.reply.id)
      expect(observed.request.params).toEqual(expect.objectContaining({ cwd: workingDir, mcpServers: setup.configuredServers }))
      expect(observed.sessionId).toBe(agent.agentSessionId)
      await use({ workspaceId, server, workingDir, agentId, ...setup })
    })
  },
})
