import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider, AgentStatus, MarkType } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'
import { withScriptedPiTool } from './helpers/scriptedPiModel'
import { readEntry, storageKeys } from './helpers/storage'
import { listAgents } from './helpers/subagentRegistry'
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
    await withScriptedPiTool(directory, 'editor_probe', {}, async (settings) => {
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
      await sendMessage(page, 'Run the configured editor probe.')
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
        return value?.choices?.['pi-dialog-text']
      }).toBe(scenario.text)
      await page.reload()
      await expect(editor).toHaveValue(scenario.text)
      await expect(page.getByTestId('queue-pause-button')).toHaveCount(0)
      const action = page.getByTestId(scenario.cancel ? 'control-deny-btn' : 'control-allow-btn')
      await expect(action).toBeInViewport({ ratio: 1 })
      if (scenario.label === 'whitespace') {
        const database = join(leapmuxServer.dataDir, 'worker', 'worker.db')
        const agentSQL = `'${agentId.replaceAll('\'', '\'\'')}'`
        execFileSync('sqlite3', [database, `CREATE TRIGGER fail_editor_response BEFORE INSERT ON messages WHEN NEW.agent_id=${agentSQL} AND NEW.mark_type=${MarkType.CONTROL_RESPONSE} BEGIN SELECT RAISE(ABORT, 'response storage unavailable'); END;`])
        try {
          await action.click()
          await expect(banner.getByRole('alert')).toContainText('response storage unavailable')
          await expect(banner.getByRole('alert')).toContainText('Could not save the response')
          await expect(banner.getByRole('status')).toContainText('The response action is complete')
          await expect(banner.getByRole('alert')).toHaveCount(1)
          await expect(editor).toHaveValue(scenario.text)
          expect(await editor.evaluate((element) => {
            const area = element.closest('[data-testid="control-banner"]')!.getBoundingClientRect()
            const input = element.getBoundingClientRect()
            return input.top >= area.top && input.bottom <= area.bottom
          })).toBe(true)
        }
        finally {
          execFileSync('sqlite3', [database, 'DROP TRIGGER IF EXISTS fail_editor_response'])
        }
        await page.reload()
        await expect(banner.getByRole('status')).toContainText('The response action is complete')
        await expect(editor).toHaveValue(scenario.text)
        await expect(messageBubbles(page).filter({ hasText: 'EDITOR_RESPONSE_RECEIVED' }).first()).toBeVisible()
        const observer = await page.context().newPage()
        try {
          await openWorkspace(observer, authenticatedEmptyWorkspace.workspaceId)
          const observedBanner = observer.getByTestId('control-banner').filter({ visible: true })
          await expect(observedBanner.getByRole('status')).toContainText('The response action is complete')
          // Stop only this isolated provider after its native tool confirms receipt.
          process.kill(Number(readFileSync(providerPID, 'utf8')), 'SIGTERM')
          await expect.poll(async () => {
            const agents = await listAgents(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, [agentId])
            return agents?.find(agent => agent.id === agentId)?.status
          }).toBe(AgentStatus.INACTIVE)
          await expect(page.getByRole('button', { name: 'Save response', exact: true })).toBeEnabled()
          await expect(observer.getByRole('button', { name: 'Save response', exact: true })).toBeEnabled()
          await page.getByRole('button', { name: 'Save response', exact: true }).click()
          await expect(observedBanner).toHaveCount(0)
        }
        finally {
          await observer.close()
        }
      }
      else {
        await action.click()
      }
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
}
