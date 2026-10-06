import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { AMP_ALLOW_ALL, ampTest } from '../amp-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { withAgentWorkspace } from '../helpers/workspace'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'
import { AMP_AGENT, nativeContext } from './scenarios'

ampTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }, testInfo) => {
  void authenticatedAmpWorkspace
  const configHome = createTestDirectory('amp-credential-profile-')
  await withCleanup(async () => {
    const receiptLog = join(configHome, 'native-credential-mcp-receipt.json')
    const { script } = writeMcpEchoServer(configHome, { receiptLog })
    const configuration = join(configHome, 'amp', 'settings.json')
    mkdirSync(dirname(configuration), { recursive: true })
    writeFileSync(configuration, JSON.stringify({ 'amp.mcpServers': { credential_probe: { command: process.execPath, args: [script] } } }))
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'amp-credential-worker',
      workerName: 'Amp credential profile',
      env: { XDG_CONFIG_HOME: configHome, AMP_SETTINGS_FILE: configuration },
    }, async ({ server, dataDir }) => {
      await withAgentWorkspace(server, { ...AMP_AGENT, prefix: 'amp-private-credential', openOptions: AMP_ALLOW_ALL }, async ({ workspaceId }) => {
        await openWorkspace(page, workspaceId)
        const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId })
        const environment = server.agentEnv
        const home = environment.HOME
        if (!home)
          throw new Error('The private Amp profile requires its HOME.')
        // Amp reads its endpoint and its key from the environment. The expected key is the suite constant, so the
        // proof compares the private environment with it.
        await exerciseCredentialIsolation(context, {
          configurationFiles: [configuration],
          inlineConfiguration: [environment.AMP_URL!, environment.AMP_API_KEY!],
          privateDirectories: [home, configHome],
          expectedCredential: MODEL_KEY,
          configurationMarkers: ['credential_probe'],
        })
        const { tools, settings } = await readAmpExecutorCatalog(context, { workerDataDir: dataDir, onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
        expect(tools).toContain('mcp__credential_probe__echo')
        expect(settings['amp.mcpServers']).toHaveProperty('credential_probe')
        const profile: unknown = JSON.parse(readFileSync(configuration, 'utf8'))
        if (!isObject(profile))
          throw new Error('The private Amp profile must contain a settings object.')
        expect(profile['amp.mcpServers']).toHaveProperty('credential_probe')
        await waitForMcpToolListed(receiptLog, 'echo')
      })
    })
  }, async () => rmSync(configHome, { recursive: true, force: true }))
})
