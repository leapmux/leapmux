import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { answerControl, controlBanner, openWorkspace, sendMessage, tabById, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { OH_MY_PI_AGENT } from './scenarios'

const PREFILL = 'Initial line\nSecond line'
const EDITOR_CASES = [
  { title: 'sends the unchanged native editor prefill', value: PREFILL },
  { title: 'sends an exact multiline native editor replacement', value: 'Changed first line\nChanged second line' },
  { title: 'sends an empty native value without cancellation', value: '' },
  { title: 'preserves whitespace in the native editor value', value: '  \n\t ' },
  { title: 'cancels the native editor without an empty value', cancel: true },
  { title: 'restores an unanswered native editor after reload', value: 'Restored editor value\nAfter reload', reload: true },
  { title: 'preserves an empty draft through a tab remount', value: '', remount: true },
  { title: 'cancels the native editor when its turn is interrupted', cancel: true, interrupt: true },
] as const

for (const scenario of EDITOR_CASES) {
  ohMyPiTest(scenario.title, async ({ authenticatedEmptyWorkspace, page, leapmuxServer }) => {
    const workingDir = newProviderWorkingDir(OH_MY_PI_AGENT, 'omp-native-editor-')
    const log = join(workingDir, 'native-editor-reply.json')
    const extensionDirectory = join(workingDir, '.omp', 'extensions')
    mkdirSync(extensionDirectory, { recursive: true })
    writeFileSync(join(extensionDirectory, 'e2e-editor.js'), `
import { writeFileSync } from 'node:fs'
export default function(pi) {
  pi.registerCommand('e2e-editor', {
    description: 'Open the isolated native editor probe.',
    handler: async (_args, ctx) => {
      const value = await ctx.ui.editor('Native E2E editor request', ${JSON.stringify(PREFILL)})
      writeFileSync(${JSON.stringify(log)}, JSON.stringify({ answered: value !== undefined, ...(value === undefined ? {} : { value }) }))
    },
  })
}
`)
    const server = leapmuxServer
    const { agentId } = await openProviderAgent(server, authenticatedEmptyWorkspace.workspaceId, OH_MY_PI_AGENT, { workingDir })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await sendMessage(page, '/e2e-editor')
    const editor = page.locator('[data-testid="dialog-editor"]:visible')
    await expect(editor).toBeVisible()
    await expect(editor).toHaveValue(PREFILL)
    await expect(editor).toHaveAccessibleName('Native E2E editor request')
    expect(existsSync(log)).toBe(false)
    if ('value' in scenario)
      await editor.fill(scenario.value)
    if ('reload' in scenario) {
      await page.reload()
      await expect(editor).toBeVisible()
      await expect(editor).toHaveValue(scenario.value)
    }
    if ('remount' in scenario) {
      const { agentId: keeperId } = await openProviderAgent(server, authenticatedEmptyWorkspace.workspaceId, OH_MY_PI_AGENT, { directoryPrefix: 'omp-editor-keeper-' })
      await tabById(page, keeperId).click()
      await tabById(page, agentId).click()
      await expect(editor).toHaveValue('')
    }
    if ('interrupt' in scenario) {
      // A pending control request hides the composer input and its Interrupt button.
      // The control banner holds the Interrupt control while the editor waits.
      await controlBanner(page).getByTestId('control-interrupt').click()
    }
    else {
      await answerControl(page, 'cancel' in scenario ? 'deny' : 'allow')
    }
    await expect.poll(() => existsSync(log)).toBe(true)
    const reply: unknown = JSON.parse(readFileSync(log, 'utf8'))
    expect(reply).toEqual('cancel' in scenario ? { answered: false } : { answered: true, value: scenario.value })
    await expect(editor).toHaveCount(0)
  })
}
