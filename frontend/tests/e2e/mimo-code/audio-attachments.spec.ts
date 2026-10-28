import type { NativeContextFixtures } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { proveNativeAudioAttachments, proveNativeAudioModelRetry } from '../helpers/nativeAudioAttachments'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { getGlobalState } from '../helpers/server'
import { mimoTest } from '../mimo-fixtures'
import { MIMO_AUDIO_MODEL_ID, mimoAudioConfiguration } from './audioConfiguration'
import { nativeContext } from './scenarios'

const TEXT_MODEL_ID = `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`

/** Add only a private native model alias, preserving the fixture's project-config restriction. */
async function withAudioCatalog(server: NativeContextFixtures['leapmuxServer'], use: () => Promise<void>): Promise<void> {
  const home = server.agentEnv?.MIMOCODE_HOME
  const inline = server.agentEnv?.MIMOCODE_CONFIG_CONTENT
  if (!home || !inline)
    throw new Error('The MiMo audio proof requires its private native configuration.')
  await withNativeConfigurationFile({
    path: join(home, 'config', 'mimocode.json'),
    content: JSON.stringify(mimoAudioConfiguration(JSON.parse(inline))),
    runDir: getGlobalState().tmpDir,
  }, use)
}

mimoTest('delivers WAV and FLAC bytes with and without prompts and retains all attachments after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withAudioCatalog(leapmuxServer, async () => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, { directoryPrefix: 'mimo-audio-', overrides: { model: MIMO_AUDIO_MODEL_ID } })
    await proveNativeAudioAttachments(context, 'openai-chat-completions')
  })
})

mimoTest('retains unsupported audio input and retries the same recording after a native model switch', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withAudioCatalog(leapmuxServer, async () => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, { directoryPrefix: 'mimo-audio-refusal-', overrides: { model: TEXT_MODEL_ID } })
    await proveNativeAudioModelRetry(context, MIMO_AUDIO_MODEL_ID, MOCK_MODELS.zai)
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
