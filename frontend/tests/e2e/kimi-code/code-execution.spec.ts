import { Buffer } from 'node:buffer'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { nativeStartupShellEnvironment } from '../helpers/nativeStartupWorker'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { hubSpawnEnv } from '../helpers/server'
import { KIMI_AGENT, kimiTest } from '../kimi-fixtures'
import { nativeContext } from './scenarios'
import { assertKimiShellCatalog, createKimiCatalogCapture, readKimiCompleteCatalog } from './toolCatalog'

kimiTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const launch = resolveNativeStartupLaunch(leapmuxServer.agentEnv, { binaryName: 'kimi' })
  const capture = createKimiCatalogCapture(createTestDirectory('kimi-native-catalog-'), launch, leapmuxServer.agentEnv)
  const environment = nativeStartupShellEnvironment(createTestDirectory('kimi-native-catalog-shell-'), capture.directory, hubSpawnEnv(leapmuxServer.agentEnv))
  await withNativeWorker(leapmuxServer, { dataDirPrefix: 'kimi-native-catalog-worker', workerName: 'Kimi native catalog test', env: environment }, async ({ server, dataDir }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeCatalogTurn(context, KIMI_AGENT)
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
