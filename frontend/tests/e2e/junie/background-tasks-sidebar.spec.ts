import { Buffer } from 'node:buffer'
import { existsSync } from 'node:fs'
import { writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { backgroundBashToolCall, junieAnswerToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from '../junie-fixtures'
import { nativeContext, runningChild } from './scenarios'

junieTest.describe('Junie subagents and background tasks', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.JUNIE

  function junieHousekeeping() {
    return [
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Subagent task' } },
      { name: 'junie-task-summary', when: { system: 'You are a task summarizer' }, respond: { text: '<summary>Junie stores sessions in Junie Home.</summary><title>Session history</title>' } },
    ]
  }

  function nativeBackgroundCommand(release: string, done: string): string {
    const source = `
const fs = require('node:fs')
const path = require('node:path')
const release = ${JSON.stringify(release)}
const done = ${JSON.stringify(done)}
let finished = false
function finish() {
  if (finished || !fs.existsSync(release)) return
  finished = true
  fs.writeFileSync(done, 'done')
  watcher.close()
}
const watcher = fs.watch(path.dirname(release), finish)
finish()
`
    const encoded = Buffer.from(source).toString('base64')
    return `node -e "eval(Buffer.from('${encoded}','base64').toString())"`
  }

  junieTest('runs a native background command without a false shell registry row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { brave_mode: 'off' })
    const release = join(workingDir, 'junie-background-release.signal')
    const done = join(workingDir, 'junie-background-done.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.rule(...junieHousekeeping())
    await modelScript.queue(
      { toolCalls: [backgroundBashToolCall(PROVIDER, 'junie-bg', nativeBackgroundCommand(release, done))] },
      { toolCalls: [junieAnswerToolCall('junie-bg-answer', 'I started the command in the background.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the command in the background.'))
    await modelScript.waitForSteps(1)
    await waitForControlBanner(page)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    expect(existsSync(done)).toBe(false)
    await expect(page.locator('[data-testid="bg-task-row"][data-kind="shell"]')).toHaveCount(0)
    await writeFile(release, 'continue\n')
    await expect.poll(() => existsSync(done)).toBe(true)
    await expect(page.locator('[data-testid="bg-task-row"][data-kind="shell"]')).toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'I started the command' }).first()).toBeVisible()
  })
})

junieTest('follows a native child from running to completed in the Background tasks sidebar', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  const child = await runningChild(context)
  await withCleanup(async () => {
    await expect(child.row).toContainText('leapmux-e2e-child')
    await expect(child.row).toHaveAttribute('data-kind', 'subagent')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(child.childId).not.toBe(child.parentId)
  }, child.finish)
  await expect(child.row).toHaveAttribute('data-status', 'completed')
})
