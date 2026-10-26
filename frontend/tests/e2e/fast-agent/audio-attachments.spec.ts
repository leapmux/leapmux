import type { NativeContextFixtures } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { expectAttachmentOutcome, sendWithAttachment, writeAudioAttachmentFixture } from '../helpers/attachments'
import { FAST_AGENT_MOCK_MODEL, GEMINI_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { proveNativeAudioAttachments } from '../helpers/nativeAudioAttachments'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { expectNativeInputRefusal } from '../helpers/nativeInputQueueRefusal'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { getGlobalState } from '../helpers/server'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { fastagentAudioConfiguration } from './audioConfiguration'
import { nativeContext } from './scenarios'

/** Preserve all existing native providers while adding the private Google route. */
async function withGoogleConfiguration(server: NativeContextFixtures['leapmuxServer'], use: () => Promise<void>): Promise<void> {
  const home = server.agentEnv?.FAST_AGENT_HOME
  const endpoint = server.agentEnv?.OPENAI_BASE_URL
  if (!home || !endpoint)
    throw new Error('The Fast Agent audio proof requires its private native environment.')
  const path = join(home, 'fast-agent.yaml')
  const runDir = getGlobalState().tmpDir
  assertPrivateNativePath(path, runDir)
  await withNativeConfigurationFile({ path, content: fastagentAudioConfiguration(readFileSync(path, 'utf8'), endpoint), runDir }, use)
}

fastAgentTest('delivers WAV and FLAC bytes with and without prompts and retains the recordings after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withGoogleConfiguration(leapmuxServer, async () => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, { directoryPrefix: 'fastagent-audio-', overrides: { model: GEMINI_MODEL_ID } })
    await proveNativeAudioAttachments(context, 'google-generative-language')
    await expect(assistantBubbles(page).filter({ hasText: 'Provider: google.' })).toHaveCount(0)
  })
})

fastAgentTest('retains audio input on an unsupported provider without making a model request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, { directoryPrefix: 'fastagent-audio-refusal-', overrides: { model: FAST_AGENT_MOCK_MODEL } })
  const name = 'unsupported-recording.wav'
  await expectAttachmentOutcome(page, 'audio', { supported: true, fileName: name, fixturePath: writeAudioAttachmentFixture(name) })
  await sendWithAttachment(page, modelScript.prompt('Listen to this recording.'))
  const failed = await expectNativeInputRefusal(context, 'does not accept audio attachments')
  await page.reload()
  expect((await expectNativeInputRefusal(context, 'does not accept audio attachments')).id).toBe(failed.id)
  const status = await modelScript.status()
  expect(status.requests).toHaveLength(0)
  expect(status.unexpectedRequests).toHaveLength(0)
})

fastAgentTest('checks the running provider after a native model switch', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withGoogleConfiguration(leapmuxServer, async () => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, { directoryPrefix: 'fastagent-audio-switch-', overrides: { model: GEMINI_MODEL_ID } })
    await sendMessage(page, `/model switch ${FAST_AGENT_MOCK_MODEL}`)
    await expect(assistantBubbles(page).filter({ hasText: `to ${FAST_AGENT_MOCK_MODEL}.` })).toHaveCount(1)
    await waitForAgentIdle(page)
    const name = 'after-native-switch.wav'
    await expectAttachmentOutcome(page, 'audio', { supported: true, fileName: name, fixturePath: writeAudioAttachmentFixture(name) })
    await sendWithAttachment(page, modelScript.prompt('The running provider must accept the recording.'))
    await expectNativeInputRefusal(context, 'does not accept audio attachments')
    const status = await modelScript.status()
    expect(status.requests).toHaveLength(0)
    expect(status.unexpectedRequests).toHaveLength(0)
    await expect(assistantBubbles(page).filter({ hasText: 'Provider: openai.' })).toHaveCount(0)
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
