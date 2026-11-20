import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, startControlEventStream } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'
import { sendActiveTerminalInput, waitForTerminalReady, waitForTerminalText } from './helpers/terminal'
import { openTerminalViaUI, terminalTabs } from './helpers/ui'

test('replays a bell that rang between CLI processes and checkpoints it', async ({ authenticatedWorkspace, leapmuxServer, page }) => {
  void authenticatedWorkspace
  await openTerminalViaUI(page)
  const tab = terminalTabs(page)
  await tab.click()
  const tabId = await tab.getAttribute('data-tab-id')
  expect(tabId).toBeTruthy()
  await waitForTerminalReady(page)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const checkpoint = join(createTestDirectory('leapmux-events-'), 'checkpoint.json')
  const args = [
    '--tab',
    tabId!,
    '--checkpoint',
    checkpoint,
    '--kind',
    'stream.ready,terminal.bell,terminal.notification',
  ]

  const first = startControlEventStream(cli, args)
  try {
    await expect.poll(() => first.events().map(event => event.kind)).toContain('stream.ready')
    await expect.poll(() => existsSync(checkpoint)).toBe(true)
  }
  finally {
    first.child.kill('SIGINT')
    await first.finished
  }
  if (process.platform !== 'win32')
    expect(await first.finished, first.diagnostic()).toEqual({ code: 0, signal: null })
  const before = JSON.parse(readFileSync(checkpoint, 'utf8')) as { sequence: number }

  // The Worker sees BEL before it reports the output that follows it.
  expect(await sendActiveTerminalInput(page, 'printf "\\aREPLAY_%s\\n" BELL\r')).toBe(true)
  await waitForTerminalText(page, 'REPLAY_BELL')

  const resumed = startControlEventStream(cli, [...args, '--limit', '1'])
  let replayedSequence: number
  try {
    await expect.poll(() => resumed.events().map(event => event.kind)).toContain('terminal.bell')
    const exit = await resumed.finished
    expect(exit, resumed.diagnostic()).toEqual({ code: 0, signal: null })
    const bells = resumed.events().filter(event => event.kind === 'terminal.bell')
    expect(bells).toEqual([
      expect.objectContaining({
        tab_id: tabId,
        replayed: true,
      }),
    ])
    const sequence = bells[0]?.sequence
    if (sequence === undefined)
      throw new Error('the replayed bell has no sequence')
    replayedSequence = sequence
    expect(replayedSequence).toBeGreaterThan(before.sequence)
    const after = JSON.parse(readFileSync(checkpoint, 'utf8')) as { sequence: number }
    expect(after.sequence).toBe(replayedSequence)
  }
  finally {
    resumed.child.kill()
    await resumed.finished
  }

  const live = startControlEventStream(cli, [...args, '--limit', '1'])
  try {
    await expect.poll(() => live.events().map(event => event.kind)).toContain('stream.ready')
    expect(await sendActiveTerminalInput(page, 'printf "\\033]9;LIVE_%s\\a" NOTICE\r')).toBe(true)
    await expect.poll(() => live.events().map(event => event.kind)).toContain('terminal.notification')
    expect(await live.finished, live.diagnostic()).toEqual({ code: 0, signal: null })
    const notifications = live.events().filter(event => event.kind === 'terminal.notification')
    expect(notifications).toEqual([
      expect.objectContaining({
        tab_id: tabId,
        body: 'LIVE_NOTICE',
        replayed: false,
      }),
    ])
    const liveSequence = notifications[0]?.sequence
    if (liveSequence === undefined)
      throw new Error('the live notification has no sequence')
    expect(liveSequence).toBeGreaterThan(replayedSequence)
    const after = JSON.parse(readFileSync(checkpoint, 'utf8')) as { sequence: number }
    expect(after.sequence).toBe(liveSequence)
  }
  finally {
    live.child.kill()
    await live.finished
  }
})
