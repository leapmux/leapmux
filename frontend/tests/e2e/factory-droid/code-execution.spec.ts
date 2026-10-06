import { cpSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { DROID_AGENT, droidTest, expect } from '../droid-fixtures'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema, openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { nativeContext } from './scenarios'
import { droidScriptConfiguration } from './scriptSettings'
import { DROID_SCRIPT_ARGUMENTS, droidCompleteToolCatalog, droidScriptExecutors } from './toolCatalog'
import { readDroidToolResult } from './toolResult'

droidTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const suiteHome = leapmuxServer.agentEnv.FACTORY_HOME_OVERRIDE
  if (!suiteHome)
    throw new Error('The native Droid Script test requires the isolated Factory home.')
  const home = createTestDirectory('droid-native-script-home-')
  const factory = join(home, '.factory')
  cpSync(join(suiteHome, '.factory'), factory, { recursive: true })
  const settingsPath = join(factory, 'settings.json')
  const settings: unknown = JSON.parse(readFileSync(settingsPath, 'utf8'))
  const configuration = droidScriptConfiguration(settings)
  writeFileSync(settingsPath, JSON.stringify(configuration.settings))
  const snapshot = join(home, 'script-feature-flags.json')
  writeFileSync(snapshot, JSON.stringify(configuration.snapshot))

  await withNativeWorker(leapmuxServer, {
    dataDirPrefix: 'droid-native-script-worker',
    workerName: 'Droid native Script test',
    env: { FACTORY_HOME_OVERRIDE: home, FACTORY_FEATURE_FLAGS_SNAPSHOT_PATH: snapshot },
  }, async ({ server }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const request = await openNativeCatalogTurn(context, DROID_AGENT, { directoryPrefix: 'native-code-execution-' })
    expect(request.mockCredential?.accepted).toBe(true)
    expect(droidScriptExecutors(droidCompleteToolCatalog(request).current).map(tool => tool.name)).toContain('Script')
    const schema = nativeCodeExecutionSchema(request, 'Script', DROID_SCRIPT_ARGUMENTS)
    expect(schema.required).toEqual(['script'])

    await exerciseNativeCodeExecution({ ...context, readToolResult: (request, callId) => readDroidToolResult(request, callId, 'Script') }, {
      catalogProof: (request) => {
        expect(request.mockCredential?.accepted).toBe(true)
        nativeCodeExecutionSchema(request, 'Script', DROID_SCRIPT_ARGUMENTS)
      },
      scripts: marker => [
        // Droid returns only what the script emits through text(). A bare expression ends with no output.
        { label: 'output', source: `text(${JSON.stringify(marker)} + (40 + 2));`, expected: `${marker}42`, failed: false },
        { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
      ],
    })
  })
})
