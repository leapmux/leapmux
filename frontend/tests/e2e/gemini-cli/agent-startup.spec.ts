import { geminiTest } from '../gemini-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { hubSpawnEnv } from '../helpers/server'
import { nativeContext } from './scenarios'

for (const failed of [false, true]) {
  geminiTest(failed ? 'retains queued input after a real native launch failure' : 'delivers input through a controlled native launch', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
    const executable = findBinary('gemini', hubSpawnEnv(leapmuxServer.agentEnv))
    if (!executable)
      throw new Error('The native Gemini executable is absent.')
    await exerciseAgentStartup(context, { launch: { binaryName: 'gemini', executable, holdWhen: ['--acp'], lazy: false }, failed })
  })
}
