import type { Page } from '@playwright/test'
import type { QueuedAgentInput } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ServerInfo } from '../fixtures'
import type { DroidModelProvider } from '../helpers/droidEnvironment'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { Buffer } from 'node:buffer'
import { appendFileSync, statSync } from 'node:fs'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { droidTest } from '../droid-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseAttachmentDelivery, expectAttachmentInRequest, expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment, writeAttachmentFixture } from '../helpers/attachments'
import { DROID_MOCK_MODEL_IDS } from '../helpers/mockAgentEnvironment'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, expectSameNativeSession, nativeOptionGroup } from '../helpers/nativeScenario'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { assistantBubbles, chooseSettingsOption, closeComposerMenus, inputQueue, openSettingsMenu, resumeQueueAfterFailure, userBubbles, waitForAgentIdle, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { prepareDroidAttachments } from './attachmentScenario'
import { DROID_AGENT, nativeContext } from './scenarios'
import { expectDroidNativeSettings } from './settingsUpdates'

droidTest.describe('attachments for Factory Droid', () => {
  for (const provider of ['anthropic', 'openai'] as const) {
    droidTest(`delivers PDF bytes through native ${provider} before and after reload`, async ({ page, leapmuxServer, modelScript }) => {
      await withDroidAttachmentWorkspace({ page, leapmuxServer, modelScript }, { primary: provider }, async (native) => {
        const before = await currentNativeAgent(native)
        expect(before.agentSessionId).not.toBe('')
        const protocol = provider === 'anthropic' ? 'anthropic-messages' : 'openai-responses'
        await exerciseAttachmentDelivery(native, 'pdf', `droid-${provider}-first.pdf`, { protocol })
        await page.reload()
        await waitForNativeSettingsHydrated(page)
        await exerciseAttachmentDelivery(native, 'pdf', `droid-${provider}-only.pdf`, { protocol, attachmentOnly: true })
        expectSameNativeSession(before, await currentNativeAgent(native), 'PDF delivery and reload')
      })
    })
  }

  droidTest('retains a refused PDF and delivers the same queued item after a compatible model change', async ({ page, leapmuxServer, modelScript }) => {
    await withDroidAttachmentWorkspace({ page, leapmuxServer, modelScript }, { primary: 'generic-chat-completion-api', alternate: 'anthropic' }, async (native) => {
      const before = await currentNativeAgent(native)
      const compatible = nativeOptionGroup(before, 'model')?.options.find(option => option.id === DROID_MOCK_MODEL_IDS.alternate)
      expect(compatible?.description).toBe('Supports PDF attachments up to 3 MiB')
      const answer = 'The retried PDF arrived.'
      const step = await modelScript.queue({ text: answer })
      const file = await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'droid-retry.pdf' })
      await sendWithAttachment(page, modelScript.prompt('Inspect this PDF after choosing a compatible model.'))
      const failed = await expectDroidPDFQueueFailure(native, 'selected Factory Droid model')
      expect(failed.attachments).toMatchObject([{ filename: basename(file), size: BigInt(statSync(file).size) }])
      const status = await modelScript.status()
      expect(status.nextStep).toBe(step)
      expect(status.unexpectedRequests).toEqual([])

      const menu = await openSettingsMenu(page, 'model')
      await menu.getByTestId(`model-${DROID_MOCK_MODEL_IDS.alternate}`).hover()
      await expect(page.getByRole('tooltip').filter({ hasText: 'Supports PDF attachments up to 3 MiB' })).toBeVisible()
      await closeComposerMenus(page)
      await chooseSettingsOption(page, `model-${DROID_MOCK_MODEL_IDS.alternate}`)
      await waitForSettingsIdle(page)
      await expectDroidNativeSettings(native, { modelId: DROID_MOCK_MODEL_IDS.alternate })
      await inputQueue(page).getByTestId(`queued-input-${failed.id}`).getByRole('button', { name: 'Retry', exact: true }).click()
      await modelScript.waitForSteps(step + 1)
      await waitForAgentIdle(page)
      const request = await modelScript.requestAt(step)
      expect(request.protocol).toBe('anthropic-messages')
      await expectAttachmentInRequest(page, request, 'pdf', file)
      await expect(inputQueue(page)).toHaveCount(0)
      await expect(userBubbles(page).filter({ hasText: basename(file) })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: answer })).toBeVisible()
      expectSameNativeSession(before, await currentNativeAgent(native), 'Retrying the PDF')
    })
  })

  droidTest('refuses an oversized PDF before native delivery and keeps the next clean turn free of its data', async ({ page, leapmuxServer, modelScript }) => {
    await withDroidAttachmentWorkspace({ page, leapmuxServer, modelScript }, { primary: 'anthropic' }, async (native) => {
      const file = writeAttachmentFixture('pdf', 'droid-oversized.pdf')
      appendFileSync(file, Buffer.alloc(3 * 1024 * 1024 + 1 - statSync(file).size, 0x20))
      const step = (await modelScript.status()).nextStep
      await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: basename(file), fixturePath: file })
      await sendWithAttachment(page, modelScript.prompt('Inspect the oversized PDF.'))
      const failed = await expectDroidPDFQueueFailure(native, '3 MiB')
      expect(failed.attachments).toMatchObject([{ filename: basename(file), size: BigInt(statSync(file).size) }])
      const status = await modelScript.status()
      expect(status.nextStep).toBe(step)
      expect(status.unexpectedRequests).toEqual([])
      const row = inputQueue(page).getByTestId(`queued-input-${failed.id}`)
      await row.getByRole('button', { name: 'Delete', exact: true }).click()
      await row.getByRole('button', { name: 'Confirm delete?', exact: true }).click()
      await expect(row).toHaveCount(0)
      await resumeQueueAfterFailure(page, 'paused')
      await expectRefusedAttachmentsAbsent(native, [file])
    })
  })
})

interface AttachmentFixtures {
  page: Page
  leapmuxServer: ServerInfo
  modelScript: ModelScript
}

async function withDroidAttachmentWorkspace(
  { page, leapmuxServer, modelScript }: AttachmentFixtures,
  routes: { primary: DroidModelProvider, alternate?: DroidModelProvider },
  run: (native: ManagedNativeScenarioContext) => Promise<void>,
): Promise<void> {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'droid-pdf',
    workerName: 'Droid PDF test',
    providerAgent: DROID_AGENT,
    prepare: directory => prepareDroidAttachments(directory, leapmuxServer.mockModelUrl, routes.primary, routes.alternate),
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(DROID_AGENT.provider)),
  }, async ({ server, workspaceId }) => {
    const native = await nativeContext({ page, leapmuxServer: server, workspaceId, modelScript })
    await waitForNativeSettingsHydrated(page)
    await run(native)
  })
}

/** Require the Worker's durable refusal before asserting absence of a model request. */
async function expectDroidPDFQueueFailure(native: ManagedNativeScenarioContext, reason: string): Promise<QueuedAgentInput> {
  const agent = await currentNativeAgent(native)
  const item = await retryUntilPass(async () => {
    const snapshot = await readNativeInputQueue(native.leapmuxServer, agent.id)
    expect(snapshot.paused).toBe(true)
    expect(snapshot.activeTurn).toBe(false)
    expect(snapshot.items).toHaveLength(1)
    const failed = snapshot.items[0]
    if (!failed)
      throw new Error('The refused PDF has no queued input item.')
    expect(failed.state).toBe(AgentInputState.FAILED)
    expect(failed.error).toContain(reason)
    return failed
  })
  await expect(inputQueue(native.page).getByTestId(`queued-input-${item.id}`)).toContainText(reason)
  return item
}
