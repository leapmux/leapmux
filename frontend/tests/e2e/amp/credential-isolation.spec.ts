import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AMP_PERMISSION_MODE } from '../../../src/generated/contracts/amp-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { ampTest } from '../amp-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { withAgentWorkspace } from '../helpers/workspace'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

ampTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }, testInfo) => {
  void authenticatedAmpWorkspace
  const configHome = createTestDirectory('amp-credential-profile-')
  await withCleanup(async () => {
    const receiptLog = join(configHome, 'native-credential-mcp-receipt.json')
    const script = writeMcpEchoServer(configHome, { receiptLog })
    const configuration = join(configHome, 'amp', 'settings.json')
    mkdirSync(dirname(configuration), { recursive: true })
    writeFileSync(configuration, JSON.stringify({ 'amp.mcpServers': { credential_probe: { command: process.execPath, args: [script] } } }))
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'amp-credential-worker',
      workerName: 'Amp credential profile',
      env: { XDG_CONFIG_HOME: configHome, AMP_SETTINGS_FILE: configuration },
    }, async ({ server, dataDir }) => {
      const defaults = agentOpenOptions(agentSettings(AgentProvider.AMP))
      await withAgentWorkspace(server, {
        provider: AgentProvider.AMP,
        prefix: 'amp-private-credential',
        openOptions: { ...defaults, optionValues: { ...defaults.optionValues, permissionMode: AMP_PERMISSION_MODE.AllowAll } },
      }, async ({ workspaceId }) => {
        await openWorkspace(page, workspaceId)
        const context = { page, modelScript, leapmuxServer: server, workspaceId, provider: AgentProvider.AMP }
        const environment = server.agentEnv
        const endpoint = environment.AMP_URL
        const credential = environment.AMP_API_KEY
        const home = environment.HOME
        if (!endpoint || !credential || !home)
          throw new Error('The private Amp profile requires its mock endpoint, credential, and HOME.')
        await exerciseCredentialIsolation(context, {
          configurationFiles: [configuration],
          inlineConfiguration: [JSON.stringify({ endpoint, apiKey: credential })],
          privateDirectories: [home, configHome],
          expectedCredential: credential,
          configurationMarkers: ['credential_probe'],
        })
        const { tools, settings } = await readAmpExecutorCatalog(context, { workerDataDir: dataDir, onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
        expect(tools).toContain('mcp__credential_probe__echo')
        expect(settings['amp.mcpServers']).toHaveProperty('credential_probe')
        const profile: unknown = JSON.parse(readFileSync(configuration, 'utf8'))
        if (!isObject(profile))
          throw new Error('The private Amp profile must contain a settings object.')
        expect(profile['amp.mcpServers']).toHaveProperty('credential_probe')
        const receipt = readMcpServerReceipt(receiptLog)
        expect(receipt.initializeCapabilities).not.toBeNull()
        expect(receipt.toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'echo'))).toBe(true)
      })
    })
  }, async () => rmSync(configHome, { recursive: true, force: true }))
})
