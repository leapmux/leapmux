import type { NativeContextFixtures } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { expectAttachmentInRequest } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment, writeAudioAttachmentFixture } from '../helpers/attachments'
import { withCleanup } from '../helpers/cleanup'
import { QWEN_ALT_MODEL_ID, QWEN_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { proveNativeAudioAttachments, proveNativeAudioModelRetry } from '../helpers/nativeAudioAttachments'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { nativeTextStep } from '../helpers/nativeScenario'
import { getGlobalState } from '../helpers/server'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { expectToastRecorded } from '../helpers/toast'
import { inputQueue, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'
import { qwenAudioConfiguration } from './audioConfiguration'
import { nativeContext } from './scenarios'

const TEXT_MODEL_ID = QWEN_ALT_MODEL_ID

/** Enable the primary private model's audio modality and restore its configuration afterward. */
async function withAudioCatalog(server: NativeContextFixtures['leapmuxServer'], use: () => Promise<void>): Promise<void> {
  const home = server.agentEnv?.QWEN_HOME
  if (!home)
    throw new Error('The Qwen audio proof requires its private native configuration.')
  const path = join(home, 'settings.json')
  assertPrivateNativePath(path, getGlobalState().tmpDir)
  await withNativeConfigurationFile({
    path,
    content: JSON.stringify(qwenAudioConfiguration(JSON.parse(readFileSync(path, 'utf8')))),
    runDir: getGlobalState().tmpDir,
  }, use)
}

qwenTest('delivers WAV and FLAC bytes with and without prompts and retains all attachments after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withAudioCatalog(leapmuxServer, async () => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    await openNativeAgent(context, {
      directoryPrefix: 'qwen-audio-',
      overrides: { model: QWEN_MODEL_ID },
    })
    await proveNativeAudioAttachments(context, 'openai-chat-completions')
  })
})

qwenTest('retains unsupported audio input and retries the same recording after a native model switch', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withAudioCatalog(leapmuxServer, async () => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    await openNativeAgent(context, {
      directoryPrefix: 'qwen-audio-refusal-',
      overrides: { model: TEXT_MODEL_ID },
    })
    await proveNativeAudioModelRetry(context, QWEN_MODEL_ID)
  })
})

qwenTest('keeps a recording queued after refusing midturn steering and delivers it once after the turn', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await withAudioCatalog(leapmuxServer, async () => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    await openNativeAgent(context, {
      directoryPrefix: 'qwen-audio-queue-',
      overrides: { model: QWEN_MODEL_ID },
    })
    const gate = 'qwen-audio-queue'
    await withCleanup(async () => {
      const step = await modelScript.queue({
        ...nativeTextStep(context, 'The first turn ended.'),
        gate,
      }, nativeTextStep(context, 'The queued recording arrived.'))
      await sendMessage(page, modelScript.prompt('Wait for the held answer.'))
      await modelScript.waitForGate(gate)
      const name = 'queued-recording.wav'
      const path = writeAudioAttachmentFixture(name)
      await expectAttachmentOutcome(page, 'audio', { supported: true, fileName: name, fixturePath: path })
      const prompt = 'QUEUEDAUDIORECORDING'
      await sendWithAttachment(page, modelScript.prompt(prompt))
      const queued = queuedInputRow(page, prompt)
      await expect(queued).toBeVisible()
      await steerButton(queued).click()
      await expect(queued).toBeVisible()
      await expectToastRecorded(page, 'agent provider does not support steering')
      const status = await modelScript.status()
      expect(status.requests).toHaveLength(1)
      await modelScript.releaseGateIfHeld(gate)
      await modelScript.waitForSteps(step + 2)
      await waitForAgentIdle(page)
      await expectAttachmentInRequest(page, await modelScript.requestAt(step + 1), 'audio', path, { mediaType: 'audio/wav' })
      await expect(inputQueue(page)).toHaveCount(0)
      await expect(userBubbles(page).filter({ hasText: name })).toHaveCount(1)
    }, async () => { await modelScript.releaseGateIfHeld(gate) })
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
