/**
 * End-to-end coverage for all 22 terminal and Quake terminal CLI commands
 * against a live Hub, Worker, PTY shell, and browser UI:
 *
 *   terminal list|get|open|send|run|read|attach|ps|wait|resize|restart|shells|close|rename|reveal,
 *   terminal quake open|close|toggle|get|read|run|send.
 */

import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI, spawnCLILive } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'
import { tabById } from './helpers/ui'

test.describe('control CLI terminals and quake terminal', () => {
  test('exercises full terminal lifecycle including send, ps, resize, attach, rename, reveal, and close', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { workspaceId } = authenticatedWorkspace

    const shells = await runCLI(cli, [
      'terminal',
      'shells',
      '--worker',
      leapmuxServer.workerId,
    ]) as {
      shells: string[]
      default_shell: string
    }
    expect(shells.shells.length).toBeGreaterThan(0)
    expect(shells.default_shell).toBeTruthy()

    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--title',
      'CLI PTY',
      '--command',
      'echo TERM_INIT_OK',
    ]) as { id: string }
    expect(opened.id).toBeTruthy()
    await expect(tabById(page, opened.id)).toBeVisible()

    const waited = await runCLI(cli, [
      'terminal',
      'wait',
      opened.id,
      '--regex',
      'TERM_INIT_OK',
      '--timeout',
      '20s',
    ]) as { reached: boolean }
    expect(waited.reached).toBe(true)

    const listed = await runCLI(cli, [
      'terminal',
      'list',
      '--workspace',
      workspaceId,
    ]) as {
      terminals: Array<{ id: string }>
    }
    expect(listed.terminals.some(t => t.id === opened.id)).toBe(true)

    const got = await runCLI(cli, ['terminal', 'get', opened.id]) as { id: string, status: string }
    expect(got.id).toBe(opened.id)

    await runCLI(cli, [
      'terminal',
      'rename',
      opened.id,
      'CLI PTY Renamed',
    ])
    await expect(tabById(page, opened.id)).toContainText('CLI PTY Renamed')

    await runCLI(cli, ['terminal', 'reveal', opened.id])
    await expect(tabById(page, opened.id)).toHaveAttribute('aria-selected', 'true')

    await runCLI(cli, [
      'terminal',
      'resize',
      opened.id,
      '100',
      '30',
    ])

    // Send raw text + Enter key via `terminal send` and verify output.
    await runCLI(cli, [
      'terminal',
      'send',
      opened.id,
      'echo SEND_MARKER_99',
    ])
    await runCLI(cli, [
      'terminal',
      'send',
      opened.id,
      '--key',
      'enter',
    ])
    await runCLI(cli, [
      'terminal',
      'wait',
      opened.id,
      '--regex',
      'SEND_MARKER_99',
      '--timeout',
      '20s',
    ])

    // Run a command via `terminal run` and read back via `terminal read`.
    await runCLI(cli, [
      'terminal',
      'run',
      opened.id,
      'echo RUN_MARKER_77',
    ])
    await runCLI(cli, [
      'terminal',
      'wait',
      opened.id,
      '--regex',
      'RUN_MARKER_77',
      '--timeout',
      '20s',
    ])
    const screen = await runCLI(cli, [
      'terminal',
      'read',
      opened.id,
      '--scrollback',
    ]) as { lines: string[] }
    expect(screen.lines.some(l => l.includes('RUN_MARKER_77'))).toBe(true)

    // Inspect foreground process via `terminal ps`.
    const ps = await runCLI(cli, ['terminal', 'ps', opened.id]) as {
      processes: Array<{ terminal_id: string, pid: number, name: string }>
    }
    expect(Array.isArray(ps.processes)).toBe(true)

    // Attach interactively via `terminal attach`, write a command, and confirm live echo.
    const attached = spawnCLILive(cli, ['terminal', 'attach', opened.id])
    try {
      attached.child.stdin?.write('echo ATTACH_LIVE_OK\r')
      await expect.poll(() => attached.stdout()).toContain('ATTACH_LIVE_OK')
    }
    finally {
      await attached.stop()
    }

    // Restart with --replace and confirm fresh shell runs cleanly.
    await runCLI(cli, [
      'terminal',
      'restart',
      opened.id,
      '--replace',
      '--command',
      'echo RESTART_OK',
    ])
    await runCLI(cli, [
      'terminal',
      'wait',
      opened.id,
      '--regex',
      'RESTART_OK',
      '--timeout',
      '20s',
    ])

    // Close via `terminal close` and confirm tab disappears in UI.
    await runCLI(cli, [
      'terminal',
      'close',
      opened.id,
      '--allow-busy',
      '--worktree',
      'keep',
    ])
    await expect(tabById(page, opened.id)).toHaveCount(0)
  })

  test('exercises all quake terminal subcommands end-to-end', async ({ authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const dir = createTestDirectory('leapmux-cli-quake-')
    // Open a tab in `dir` first so the Worker allows a Quake terminal bound to `dir`.
    const anchor = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--dir',
      dir,
    ]) as { id: string }
    const targetFlags = [
      '--dir',
      dir,
      '--worker',
      leapmuxServer.workerId,
    ]

    const opened = await runCLI(cli, [
      'terminal',
      'quake',
      'open',
      ...targetFlags,
    ]) as {
      open?: boolean
      id?: string
      terminal_id?: string
    }
    expect(opened).toBeTruthy()

    const got = await runCLI(cli, [
      'terminal',
      'quake',
      'get',
      ...targetFlags,
    ])
    expect(got).toBeTruthy()

    await runCLI(cli, [
      'terminal',
      'quake',
      'run',
      'echo QUAKE_RUN_123',
      ...targetFlags,
    ])
    await expect.poll(async () => {
      const read = await runCLI(cli, [
        'terminal',
        'quake',
        'read',
        '--lines',
        '50',
        ...targetFlags,
      ]) as {
        lines?: string[]
        text?: string
      }
      return (read.lines ?? []).join('\n') + (read.text ?? '')
    }).toContain('QUAKE_RUN_123')

    await runCLI(cli, [
      'terminal',
      'quake',
      'send',
      'echo QUAKE_SEND_456',
      ...targetFlags,
    ])
    await runCLI(cli, [
      'terminal',
      'quake',
      'send',
      '--key',
      'enter',
      ...targetFlags,
    ])
    await expect.poll(async () => {
      const read = await runCLI(cli, [
        'terminal',
        'quake',
        'read',
        '--scrollback',
        ...targetFlags,
      ]) as {
        lines?: string[]
        text?: string
      }
      return (read.lines ?? []).join('\n') + (read.text ?? '')
    }).toContain('QUAKE_SEND_456')

    await runCLI(cli, [
      'terminal',
      'quake',
      'toggle',
      ...targetFlags,
    ])
    await runCLI(cli, [
      'terminal',
      'quake',
      'toggle',
      ...targetFlags,
    ])
    await runCLI(cli, [
      'terminal',
      'quake',
      'close',
      ...targetFlags,
    ])
    await runCLI(cli, [
      'terminal',
      'close',
      anchor.id,
      '--allow-busy',
      '--worktree',
      'keep',
    ])
  })
})
