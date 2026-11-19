import { randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { OPTION_ID_EFFORT } from '../../../src/components/chat/settingsGroups'
import { AgentInputKind, WorkerAgentInputService } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { expectAttachmentOutcome, writeAudioAttachmentFixture } from '../helpers/attachments'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { expectNativeInputRefusal } from '../helpers/nativeInputQueueRefusal'
import { OPENCODE_AUDIO_MODEL_ID, withOpenCodeAudioCatalog } from '../helpers/openCodeAudioConfiguration'
import { composerEditor } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'
import { nativeContext } from './scenarios'

// OpenCode's session converter discards audio before model input, even for a Google audio model.
// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
for (const format of ['wav', 'flac'] as const) {
  for (const attachmentOnly of [false, true]) {
    opencodeTest(`refuses ${format} ${attachmentOnly ? 'without a prompt' : 'with a prompt'} and retains the refused recording after reload`, async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
      await withOpenCodeAudioCatalog(leapmuxServer, 'opencode', async () => {
        const context = await nativeContext({
          page,
          modelScript,
          leapmuxServer,
          workspaceId: authenticatedEmptyWorkspace.workspaceId,
        })
        const { agentId } = await openNativeAgent(context, {
          directoryPrefix: 'opencode-audio-refusal-',
          overrides: {
            model: OPENCODE_AUDIO_MODEL_ID,
            optionValues: { [OPTION_ID_EFFORT]: 'auto' },
          },
        })
        const name = `refused-recording.${format}`
        const path = writeAudioAttachmentFixture(name, format)
        const original = new Uint8Array(readFileSync(path))
        const text = attachmentOnly
          ? ''
          : modelScript.prompt('Keep this recording until a provider can accept it.')
        if (text)
          await composerEditor(page).fill(text)
        await expectAttachmentOutcome(page, 'audio', { supported: false, fileName: name, fixturePath: path })
        await expect(composerEditor(page)).toHaveText(text, { useInnerText: true })

        // A direct client submission still reaches the Worker's durable queue. Its refusal must preserve the bytes.
        const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
        const inputId = randomUUID()
        await channel.callWorker(leapmuxServer.workerId, WorkerAgentInputService.method.enqueueAgentInput, {
          agentId,
          inputId,
          text,
          kind: AgentInputKind.USER_MESSAGE,
          attachments: [
            { filename: name, mimeType: `audio/${format}`, data: original },
          ],
        })
        const reason = 'OpenCode does not support audio attachments'
        const refused = await expectNativeInputRefusal(context, reason)
        expect(refused.id).toBe(inputId)
        await page.reload()
        expect((await expectNativeInputRefusal(context, reason)).id).toBe(inputId)

        const clientId = randomUUID()
        const edit = await channel.callWorker(leapmuxServer.workerId, WorkerAgentInputService.method.beginQueuedAgentInputEdit, { agentId, inputId, clientId })
        try {
          expect(edit.text).toBe(text)
          expect(edit.attachments).toHaveLength(1)
          expect(edit.attachments[0]?.filename).toBe(name)
          expect(edit.attachments[0]?.data).toEqual(original)
        }
        finally {
          await channel.callWorker(leapmuxServer.workerId, WorkerAgentInputService.method.cancelQueuedAgentInputEdit, { agentId, inputId, clientId })
        }
        const status = await modelScript.status()
        expect(status.requests).toHaveLength(0)
        expect(status.unexpectedRequests).toHaveLength(0)
      })
    })
  }
}
