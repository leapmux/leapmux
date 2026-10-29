import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment, writeAudioAttachmentFixture } from '../helpers/attachments'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { getGlobalState } from '../helpers/server'
import { queuedInputRow } from '../helpers/steer'
import { userBubbles } from '../helpers/ui'
import { codexAudioModelCatalog } from './audioCatalog'
import { codexAudioInputs } from './audioInput'
import { nativeContext } from './scenarios'

codexTest('delivers real audio bytes with and without a prompt and retains the attachments after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const runDir = getGlobalState().tmpDir
  const home = leapmuxServer.agentEnv.CODEX_HOME
  if (!home)
    throw new Error('The native Codex audio proof requires its private configuration directory.')
  const configPath = join(home, 'config.toml')
  const catalogPath = join(runDir, 'codex-audio-model-catalog.json')
  const config = `model_catalog_json = ${JSON.stringify(catalogPath)}\n${readFileSync(configPath, 'utf8')}`
  await withNativeConfigurationFile({
    path: catalogPath,
    content: JSON.stringify(codexAudioModelCatalog(MOCK_MODELS.openai)),
    runDir,
  }, async () => {
    await withNativeConfigurationFile({ path: configPath, content: config, runDir }, async () => {
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer,
        workspaceId: authenticatedEmptyWorkspace.workspaceId,
      })
      await openNativeAgent(context, { directoryPrefix: 'codex-audio-' })
      for (const attachmentOnly of [false, true]) {
        const name = attachmentOnly ? 'voice-only.wav' : 'voice-with-prompt.wav'
        const path = writeAudioAttachmentFixture(name)
        await exerciseAttachmentDelivery(context, 'audio', name, {
          fixturePath: path,
          attachmentOnly,
          protocol: 'openai-responses',
          proof: (request, source) => {
            expect(request.mockCredential?.accepted).toBe(true)
            const parts = codexAudioInputs(request)
            expect(parts).toHaveLength(1)
            expect(parts[0]?.mimeType).toBe('audio/wav')
            expect(parts[0]?.bytes).toEqual(readFileSync(source))
          },
        })
      }
      await page.reload()
      for (const name of ['voice-with-prompt.wav', 'voice-only.wav'])
        await expect(userBubbles(page).filter({ hasText: name })).toHaveCount(1)
    })
  })
})

codexTest('retains a clear delivery error when the selected model does not accept audio', async ({ native }) => {
  const name = 'unsupported-model.wav'
  const path = writeAudioAttachmentFixture(name)
  await expectAttachmentOutcome(native.page, 'audio', { supported: true, fileName: name, fixturePath: path })
  const marker = 'CODEX_AUDIO_UNSUPPORTED_MODEL'
  await sendWithAttachment(native.page, native.modelScript.prompt(marker))
  for (const reload of [false, true]) {
    if (reload)
      await native.page.reload()
    await expect(queuedInputRow(native.page, marker)).toContainText('does not accept audio attachments')
    const status = await native.modelScript.status()
    expect(status.requests).toHaveLength(0)
    expect(status.unexpectedRequests).toHaveLength(0)
  }
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
