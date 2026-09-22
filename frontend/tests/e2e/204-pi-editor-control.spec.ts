import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { withMockModelScenario } from './helpers/mockModelScenario'
import { createTestDirectory } from './helpers/runDirectory'
import { withMockPiModel } from './helpers/scriptedPiModel'
import { readEntry, storageKeys } from './helpers/storage'
import { expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

for (const scenario of [
  { label: 'whitespace', text: '  first line\n\tsecond line\n  ', cancel: false },
  { label: 'empty text', text: '', cancel: false },
  { label: 'cancellation', text: '  unsent text\n', cancel: true },
]) {
  test(`delivers a native Pi editor answer after reload with ${scenario.label}`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.setViewportSize({ width: 780, height: 1000 })
    const directory = createTestDirectory('pi-editor-control-')
    const receipt = join(directory, 'editor-receipt.txt')
    const providerPID = join(directory, 'provider.pid')
    await withMockPiModel(directory, leapmuxServer.mockModelUrl, async (settings) => {
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
      await withMockModelScenario(leapmuxServer.mockModelUrl, [
        { toolCalls: [{ id: 'editor-call', name: 'editor_probe', arguments: {} }] },
        { text: 'Protocol test complete.' },
      ], async (modelScenario) => {
        await sendMessage(page, modelScenario.prompt('Run the configured editor probe.'))
        const banner = page.getByTestId('control-banner').filter({ visible: true })
        const editor = page.getByTestId('pi-editor')
        await expect(banner).toContainText('Edit the probe text')
        await expect(banner.getByTestId('pi-editor')).toBeVisible()
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
        await expect.poll(async () => {
          const key = (await storageKeys(page)).find(key => key.includes(`control-state:${agentId}:`))
          const value = key ? (await readEntry(page, key))?.v as { choices?: Record<string, string> } | undefined : undefined
          return value?.choices?.['dialog-text']
        }).toBe(scenario.text)
        await page.reload()
        await expect(editor).toHaveValue(scenario.text)
        await expect(page.getByTestId('queue-pause-button')).toHaveCount(0)
        const action = page.getByTestId(scenario.cancel ? 'control-deny-btn' : 'control-allow-btn')
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
        await expect(banner).toHaveCount(0)
        await waitForAgentIdle(page, 120_000)
        await expect(page.getByTestId('composer-editor')).toBeVisible()
        await expect(page.getByTestId('composer-editor').locator('.ProseMirror')).toHaveAttribute('contenteditable', 'true')
        await expect(messageBubbles(page).filter({ hasText: 'EDITOR_RESPONSE_RECEIVED' }).first()).toBeVisible()
        expect(JSON.parse(readFileSync(receipt, 'utf8'))).toEqual(scenario.cancel
          ? { cancelled: true }
          : { value: scenario.text, cancelled: false })
        if (scenario.label === 'whitespace') {
          const answer = page.getByTestId('control-response-text').filter({ hasText: 'first line' })
          expect(await answer.textContent()).toBe(scenario.text)
          expect(await answer.evaluate((element) => {
            const range = document.createRange()
            range.setStart(element.firstChild!, 0)
            range.setEnd(element.firstChild!, 2)
            return range.getBoundingClientRect().width
          })).toBeGreaterThan(0)
        }
        else if (scenario.label === 'empty text') {
          await expect(page.getByTestId('control-response-text')).toHaveText('Empty answer')
        }
        expect(errors).toEqual([])
      })
    })
  })
}
