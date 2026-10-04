import { Buffer } from 'node:buffer'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeStartupShellEnvironment } from '../helpers/nativeStartupWorker'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { hubSpawnEnv } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { assertKimiShellCatalog, createKimiCatalogCapture, readKimiCompleteCatalog } from './toolCatalog'

kimiTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const launch = resolveNativeStartupLaunch(leapmuxServer.agentEnv, { binaryName: 'kimi' })
  const capture = createKimiCatalogCapture(createTestDirectory('kimi-native-catalog-'), launch, leapmuxServer.agentEnv)
  const environment = nativeStartupShellEnvironment(createTestDirectory('kimi-native-catalog-shell-'), capture.directory, hubSpawnEnv(leapmuxServer.agentEnv))
  await withNativeWorker(leapmuxServer, { dataDirPrefix: 'kimi-native-catalog-worker', workerName: 'Kimi native catalog test', env: environment }, async ({ server, dataDir }) => {
    const context = { page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
    await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, context.workspaceId, createTestDirectory('native-code-limit-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
    await openWorkspace(page, context.workspaceId)
    await sendNativeAnswer(context, 'Reply once while the native tool registry remains available.', 'The actual native registry turn completed.')
    // The owned native route returns every registry entry, including inactive tools that the model cannot see.
    const catalog = await readKimiCompleteCatalog(context, capture, dataDir)
    await testInfo.attach('kimi-complete-native-registry', {
      body: Buffer.from(JSON.stringify(catalog.map(({ name, source, active }) => ({ name, source, active })), null, 2)),
      contentType: 'application/json',
    })
    assertKimiShellCatalog(catalog)
    await exerciseShellToolExecution(context, { includeFailure: false })
  })
})
