import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { accountStorageKey, PREFIX_CONTROL_STATE } from '../../../src/lib/browserStorage'
import { isObject } from '../../../src/lib/jsonPick'
import { openAgentViaAPI } from '../helpers/api'
import { expectTurnEndedAfter } from '../helpers/modelScriptFixture'
import { waitForNativeInputQueueIdle } from '../helpers/nativeInputQueueIdle'
import { piEditorProbeToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { waitForStoredEntry } from '../helpers/storage'
import { composerEditor, controlBanner, controlButton, expectNoControlBanner, expectSettingsChip, messageBubbles, openWorkspace, resumePausedQueue, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { PI_AGENT } from './scenarios'
import { withMockPiModel } from './scriptedModel'

for (const scenario of [
  { label: 'whitespace', text: '  first line\n\tsecond line\n  ', cancel: false },
  { label: 'empty text', text: '', cancel: false },
  { label: 'cancellation', text: '  unsent text\n', cancel: true },
  { label: 'interruption', text: '  interrupted draft\n', cancel: true, interrupt: true },
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
      const agentId = await openAgentViaAPI(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, directory, {
        agentProvider: AgentProvider.PI,
        ...settings,
      })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await expectSettingsChip(page, 'Protocol test')
      const start = await modelScript.queue(
        { toolCalls: [piEditorProbeToolCall('editor-call')] },
        ...('interrupt' in scenario ? [] : [{ text: 'Protocol test complete.' }]),
      )
      await sendMessage(page, modelScript.prompt('Run the configured editor probe.'))
      const banner = controlBanner(page)
      const editor = banner.getByTestId('dialog-editor')
      await expect(banner).toContainText('Edit the probe text')
      await expect(editor).toBeVisible()
      // composerEditor selects visible elements. Read the actual composer to prove that it hides.
      await expect(page.getByTestId('composer-editor')).toBeHidden()
      expect(await editor.evaluate((element) => {
        const banner = element.closest('[data-testid="control-banner"]')!
        const style = getComputedStyle(banner)
        const width = banner.clientWidth - Number.parseFloat(style.paddingLeft) - Number.parseFloat(style.paddingRight)
        return Math.abs(element.getBoundingClientRect().width - width) <= 1
      })).toBe(true)
      await expect(editor).toHaveValue('Original prefill')
      await editor.fill(scenario.text)
      // Require the saved draft before the reload.
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
      // This native proof uses successful storage writes.
      // A failed answer write needs a separate fault injection that keeps the unanswered dialog.
      // See https://github.com/leapmux/leapmux/issues/489.
      if ('interrupt' in scenario)
        await banner.getByTestId('control-interrupt').click()
      else
        await action.click()
      await expectNoControlBanner(page)
      const completedSteps = start + ('interrupt' in scenario ? 1 : 2)
      await modelScript.waitForSteps(completedSteps)
      await waitForAgentIdle(page)
      await expect.poll(() => {
        try {
          return JSON.parse(readFileSync(receipt, 'utf8'))
        }
        catch (error) {
          if ((error as NodeJS.ErrnoException).code === 'ENOENT')
            return null
          throw error
        }
      }).toEqual(scenario.cancel ? { cancelled: true } : { value: scenario.text, cancelled: false })
      await expectTurnEndedAfter(modelScript, completedSteps)
      await expect(composerEditor(page)).toHaveAttribute('contenteditable', 'true')
      if ('interrupt' in scenario) {
        const queue = await waitForNativeInputQueueIdle(leapmuxServer, agentId, modelScript.testDeadline)
        expect(queue.paused).toBe(true)
        await resumePausedQueue(page)
        const next = await modelScript.queue({ text: 'The editor interrupt ended. The next turn works.' })
        await sendMessage(page, modelScript.prompt('Reply after the editor interrupt.'))
        await modelScript.waitForSteps(next + 1)
        await waitForAgentIdle(page)
        await expect(messageBubbles(page).filter({ hasText: 'The editor interrupt ended. The next turn works.' }).first()).toBeVisible()
        await expectTurnEndedAfter(modelScript, next + 1)
        await expectNoControlBanner(page)
      }
      else {
        await expect(messageBubbles(page).filter({ hasText: 'EDITOR_RESPONSE_RECEIVED' }).first()).toBeVisible()
      }
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
