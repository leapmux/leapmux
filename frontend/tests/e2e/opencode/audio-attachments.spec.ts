import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { proveNativeAudioAttachments, proveNativeAudioModelRetry } from '../helpers/nativeAudioAttachments'
import { OPENCODE_AUDIO_MODEL_ID, withOpenCodeAudioCatalog } from '../helpers/openCodeAudioConfiguration'
import { opencodeTest } from '../opencode-fixtures'
import { nativeContext } from './scenarios'

opencodeTest('delivers WAV and FLAC bytes with and without prompts and retains the recordings after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withOpenCodeAudioCatalog(leapmuxServer, 'opencode', async () => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, { directoryPrefix: 'opencode-audio-', overrides: { model: OPENCODE_AUDIO_MODEL_ID } })
    await proveNativeAudioAttachments(context, 'google-generative-language')
  })
})

opencodeTest('retains unsupported audio input and retries it after a native model switch', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withOpenCodeAudioCatalog(leapmuxServer, 'opencode', async (textModelID) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, { directoryPrefix: 'opencode-audio-refusal-', overrides: { model: textModelID } })
    await proveNativeAudioModelRetry(context, OPENCODE_AUDIO_MODEL_ID)
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
