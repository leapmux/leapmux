import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { accountStorageKey, PREFIX_CONTROL_STATE } from '../../../src/lib/browserStorage'
import { isObject } from '../../../src/lib/jsonPick'
import { openAgentViaAPI } from '../helpers/api'
import { piEditorProbeToolCall } from '../helpers/providerToolCalls'
import { waitForStoredEntry } from '../helpers/storage'
import { composerEditor, controlBanner, controlButton, expectNoControlBanner, expectSettingsChip, messageBubbles, openWorkspace, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { newProviderWorkingDir } from '../helpers/workspace'
import { piTest } from '../pi-fixtures'
import { PI_AGENT } from './scenarios'
import { withMockPiModel } from './scriptedModel'

for (const scenario of [
  { label: 'whitespace', text: '  first line\n\tsecond line\n  ', cancel: false },
  { label: 'empty text', text: '', cancel: false },
  { label: 'cancellation', text: '  unsent text\n', cancel: true },
]) {
  piTest(`delivers a native Pi editor answer after reload with ${scenario.label}`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.setViewportSize({ width: 780, height: 1000 })
    const directory = newProviderWorkingDir(PI_AGENT, 'pi-editor-control-')
    const receipt = join(directory, 'editor-receipt.txt')
    const providerPID = join(directory, 'provider.pid')
    await withMockPiModel(directory, leapmuxServer, async (settings) => {
      writeFileSync(join(directory, '.pi', 'extensions', 'editor-probe.ts'), `
import { writeFileSync } from 'node:fs';
export default function (pi) {
  pi.registerTool({
    name: 'editor_probe', label: 'Editor probe', description: 'Request the protocol test editor.',
    parameters: { type: 'object', properties: {}, required: [] },
    async execute(_id, _params, _signal, _update, ctx) {
      writeFileSync(${JSON.stringify(providerPID)}, String(process.pid));
      const value = await ctx.ui.editor('Edit the probe text', 'Original prefill');
      writeFileSync(${JSON.stringify(receipt)}, JSON.stringify({ value, cancelled: value === undefined }));
      return { content: [{ type: 'text', text: 'EDITOR_RESPONSE_RECEIVED' }], details: {} };
    },
  });
}
`)
      const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
        agentProvider: AgentProvider.PI,
        ...settings,
      })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await expectSettingsChip(page, 'Protocol test')
      const start = await modelScript.queue(
        { toolCalls: [piEditorProbeToolCall('editor-call')] },
        { text: 'Protocol test complete.' },
      )
      await sendMessage(page, modelScript.prompt('Run the configured editor probe.'))
      const banner = controlBanner(page)
      const editor = banner.getByTestId('dialog-editor')
      await expect(banner).toContainText('Edit the probe text')
      await expect(editor).toBeVisible()
      // `composerEditor` matches only a visible composer, so a hidden check through it passes always.
      await expect(page.getByTestId('composer-editor')).toBeHidden()
      expect(await editor.evaluate((element) => {
        const banner = element.closest('[data-testid="control-banner"]')!
        const style = getComputedStyle(banner)
        const width = banner.clientWidth - Number.parseFloat(style.paddingLeft) - Number.parseFloat(style.paddingRight)
        return Math.abs(element.getBoundingClientRect().width - width) <= 1
      })).toBe(true)
      await expect(editor).toHaveValue('Original prefill')
      await editor.fill(scenario.text)
      // Verify the committed draft before reload tests recovery.
      await waitForStoredEntry(
        page,
        accountStorageKey(leapmuxServer.adminUserId, `${PREFIX_CONTROL_STATE}${agentId}:`),
        value => isObject(value) && isObject(value.choices) && value.choices['dialog-text'] === scenario.text,
        'the editor draft must be persisted before the reload',
      )
      await page.reload()
      await expect(editor).toHaveValue(scenario.text)
      await expect(page.getByTestId('queue-pause-button')).toHaveCount(0)
      const action = controlButton(page, scenario.cancel ? 'deny' : 'allow')
      await expect(action).toBeInViewport({ ratio: 1 })
      // One path for every scenario. The `whitespace` case used to branch here
      // into a storage-failure flow: a SQLite trigger aborted the response
      // write, the banner then STAYED with an alert, and a second tab watched
      // that unsaved state. All of it rested on the injected failure -- with
      // the write succeeding the banner closes at once -- so the branch went
      // with the injection rather than being rewritten around a state the
      // product no longer reaches here. See
      // https://github.com/leapmux/leapmux/issues/489.
      await action.click()
      await expectNoControlBanner(page)
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      await expect(composerEditor(page)).toHaveAttribute('contenteditable', 'true')
      await expect(messageBubbles(page).filter({ hasText: 'EDITOR_RESPONSE_RECEIVED' }).first()).toBeVisible()
      expect(JSON.parse(readFileSync(receipt, 'utf8'))).toEqual(scenario.cancel
        ? { cancelled: true }
        : { value: scenario.text, cancelled: false })
      if (scenario.label === 'whitespace') {
        const answer = savedControlAnswer(page).filter({ hasText: 'first line' })
        expect(await answer.textContent()).toBe(scenario.text)
        expect(await answer.evaluate((element) => {
          const range = document.createRange()
          range.setStart(element.firstChild!, 0)
          range.setEnd(element.firstChild!, 2)
          return range.getBoundingClientRect().width
        })).toBeGreaterThan(0)
      }
      else if (scenario.label === 'empty text') {
        await expect(savedControlAnswer(page)).toHaveText('Empty answer')
      }
      expect(errors).toEqual([])
    })
  })
}
