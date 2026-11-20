import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI, startControlEventStream } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'
import { sendActiveTerminalInput, waitForTerminalReady, waitForTerminalText } from './helpers/terminal'
import { openTerminalViaUI, tabById, terminalTabs } from './helpers/ui'

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

test('replays a closed tab to its final Worker position', async ({ authenticatedWorkspace, leapmuxServer, page }) => {
  void authenticatedWorkspace
  await openTerminalViaUI(page)
  const tab = terminalTabs(page)
  await tab.click()
  const tabId = await tab.getAttribute('data-tab-id')
  if (!tabId)
    throw new Error('the terminal tab has no ID')
  await waitForTerminalReady(page)

  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const checkpoint = join(createTestDirectory('leapmux-closed-tab-events-'), 'checkpoint.json')
  const args = [
    '--tab',
    tabId,
    '--checkpoint',
    checkpoint,
    '--kind',
    'stream,terminal.bell,terminal.exit',
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

  expect(await sendActiveTerminalInput(page, 'printf "\\aCLOSED_%s\\n" BELL\r')).toBe(true)
  await waitForTerminalText(page, 'CLOSED_BELL')
  expect(await sendActiveTerminalInput(page, 'exit 27\r')).toBe(true)
  const shell = await runCLI(cli, [
    'terminal',
    'wait',
    tabId,
    '--exit',
    '--timeout',
    '20s',
  ])
  expect(shell).toMatchObject({ reached: true, exit_code: 27 })
  await expect(tabById(page, tabId)).toBeVisible()
  const closed = await runCLI(cli, [
    'tab',
    'close',
    tabId,
    '--worktree',
    'keep',
  ]) as {
    tombstoned: boolean
    worker_close_error?: string
  }
  expect(closed.tombstoned).toBe(true)
  expect(closed.worker_close_error).toBeUndefined()

  const resumed = startControlEventStream(cli, args)
  try {
    expect(await resumed.finished, resumed.diagnostic()).toEqual({ code: 0, signal: null })
    const events = resumed.events()
    expect(events.map(event => event.kind)).toEqual([
      'stream.ready',
      'terminal.bell',
      'terminal.exit',
      'stream.retired',
    ])
    expect(events[1]).toMatchObject({ tab_id: tabId, replayed: true })
    expect(events[2]).toMatchObject({
      tab_id: tabId,
      exit_code: 27,
      replayed: true,
    })
    expect(events[3]).toMatchObject({
      tab_id: tabId,
      sequence: expect.any(Number),
    })
    const saved = JSON.parse(readFileSync(checkpoint, 'utf8')) as { sequence: number }
    expect(saved.sequence).toBe(events[3]?.sequence)
  }
  finally {
    resumed.child.kill()
    await resumed.finished
  }

  const completed = startControlEventStream(cli, args)
  try {
    expect(await completed.finished, completed.diagnostic()).toEqual({ code: 0, signal: null })
    expect(completed.events().map(event => event.kind)).toEqual(['stream.ready', 'stream.retired'])
  }
  finally {
    completed.child.kill()
    await completed.finished
  }
})

test('replays a tab opened, moved out and back, and closed while disconnected', async ({ authenticatedWorkspace, leapmuxServer, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const directory = createTestDirectory('leapmux-workspace-events-')
  const checkpoint = join(directory, 'workspace.json')
  const accountCheckpoint = join(directory, 'account.json')
  const { workspaceId } = authenticatedWorkspace
  const anchor = await runCLI(cli, [
    'terminal',
    'open',
    '--workspace',
    workspaceId,
    '--worker',
    leapmuxServer.workerId,
  ]) as { id: string, tile_id: string }
  const args = [
    '--workspace',
    workspaceId,
    '--checkpoint',
    checkpoint,
    '--kind',
    'stream,tab.opened,tab.moved,tab.closed',
  ]
  const accountArgs = [
    '--checkpoint',
    accountCheckpoint,
    '--kind',
    'stream.ready,tab.moved,workspace.deleted,tab.closed',
  ]
  for (const [startArgs, path] of [
    [args, checkpoint],
    [accountArgs, accountCheckpoint],
  ] as const) {
    const initial = startControlEventStream(cli, startArgs)
    try {
      await expect.poll(() => initial.events().map(event => event.kind)).toContain('stream.ready')
      await expect.poll(() => existsSync(path)).toBe(true)
    }
    finally {
      initial.child.kill('SIGINT')
      await initial.finished
    }
  }

  const other = await runCLI(cli, ['workspace', 'create', 'Replay move destination']) as { id: string }
  const terminal = await runCLI(cli, [
    'terminal',
    'open',
    '--workspace',
    workspaceId,
    '--worker',
    leapmuxServer.workerId,
  ]) as { id: string }
  await expect(tabById(page, terminal.id)).toBeVisible()
  for (const [flag, destination] of [
    ['--workspace', other.id],
    ['--tile', anchor.tile_id],
    ['--workspace', other.id],
    ['--before', anchor.id],
  ] as const) {
    await runCLI(cli, [
      'tab',
      'move',
      terminal.id,
      flag,
      destination,
    ])
  }
  await runCLI(cli, ['workspace', 'delete', other.id])
  const closed = await runCLI(cli, [
    'tab',
    'close',
    terminal.id,
    '--worktree',
    'keep',
    '--allow-busy',
  ]) as { tombstoned: boolean, worker_close_error?: string }
  expect(closed.tombstoned).toBe(true)
  expect(closed.worker_close_error).toBeUndefined()
  await expect(tabById(page, terminal.id)).toHaveCount(0)

  const resumed = startControlEventStream(cli, [...args, '--limit', '6'])
  try {
    expect(await resumed.finished, resumed.diagnostic()).toEqual({ code: 0, signal: null })
    const facts = resumed.events().filter(event => event.kind.startsWith('tab.'))
    const entered = expect.objectContaining({
      kind: 'tab.opened',
      tab_id: terminal.id,
      workspace_id: workspaceId,
      replayed: true,
    })
    const left = expect.objectContaining({
      kind: 'tab.moved',
      tab_id: terminal.id,
      from_workspace_id: workspaceId,
      replayed: true,
    })
    expect(facts).toEqual([
      entered,
      left,
      entered,
      left,
      entered,
      expect.objectContaining({
        kind: 'tab.closed',
        tab_id: terminal.id,
        workspace_id: workspaceId,
        replayed: true,
      }),
    ])
    for (const index of [1, 3])
      expect(facts[index]).not.toHaveProperty('workspace_id')
    for (const index of [2, 4])
      expect(facts[index]).not.toHaveProperty('from_workspace_id')
  }
  finally {
    resumed.child.kill()
    await resumed.finished
  }

  const account = startControlEventStream(cli, [...accountArgs, '--limit', '6'])
  try {
    expect(await account.finished, account.diagnostic()).toEqual({ code: 0, signal: null })
    const moved = (from: string, to: string) => expect.objectContaining({
      kind: 'tab.moved',
      tab_id: terminal.id,
      from_workspace_id: from,
      workspace_id: to,
      replayed: true,
    })
    expect(account.events().filter(event => !event.kind.startsWith('stream.'))).toEqual([
      moved(workspaceId, other.id),
      moved(other.id, workspaceId),
      moved(workspaceId, other.id),
      moved(other.id, workspaceId),
      expect.objectContaining({ kind: 'workspace.deleted', workspace_id: other.id, replayed: true }),
      expect.objectContaining({
        kind: 'tab.closed',
        tab_id: terminal.id,
        workspace_id: workspaceId,
        replayed: true,
      }),
    ])
  }
  finally {
    account.child.kill()
    await account.finished
  }
})
