/**
 * End-to-end coverage of the terminal commands of `leapmux control` that a
 * real shell decides: startup commands, replacement and exit codes.
 *
 * Go tests cover the request, the readiness rule and the exit-code record with
 * a recording shell and fake Workers. This spec runs the CLI against the real
 * Hub, Worker and login shell, and observes the result in the CLI and in the
 * browser.
 *
 * Each `terminal wait` passes a `--timeout` below the 30-second limit of
 * `runCLI`, so a wait that does not end fails with the CLI's own message.
 */

import { spawnSync } from 'node:child_process'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { TerminalStatus } from '../../src/generated/proto/leapmux/v1/terminal_pb'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { waitForTerminalText } from './helpers/terminal'
import { openNewTerminalDialog, tabById, workspaceRow } from './helpers/ui'
import { waitForTerminalExitViaAPI } from './helpers/workerTabs'

/** The result of `terminal open`. */
interface OpenedTerminal {
  id: string
}

/** The result of `terminal wait`. */
interface TerminalWaitResult {
  id: string
  status: string
  reached: boolean
  exit_code?: number
  line?: string
}

/** The result of `terminal read`. */
interface RenderedScreen {
  lines: string[]
}

/** The result of `terminal get`. */
interface TerminalRecord {
  status: string
  exit_code?: number
}

test.describe('control CLI terminals', () => {
  test('lists installed Nushell and Elvish in the CLI and terminal picker', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const installed = [
      { name: 'nu', version: '--version' },
      { name: 'elvish', version: '-version' },
    ].filter(shell => spawnSync(shell.name, [shell.version], { stdio: 'ignore' }).status === 0).map(shell => shell.name)
    test.skip(installed.length === 0, 'Neither Nushell nor Elvish is installed on the test host.')

    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const listed = await runCLI(cli, [
      'terminal',
      'shells',
      '--worker',
      leapmuxServer.workerId,
    ]) as { shells: string[], default_shell: string }
    const shellName = (path: string) => basename(path).replace(/\.exe$/i, '')
    const paths = listed.shells.filter(path => installed.includes(shellName(path)))
    expect(paths.map(shellName)).toEqual(expect.arrayContaining(installed))

    await expect(workspaceRow(page, authenticatedWorkspace.workspaceId)).toBeVisible()
    await openNewTerminalDialog(page)
    const dialog = page.getByRole('dialog', { name: 'New Terminal' })
    await dialog.getByTestId('shell-select-menu-trigger').click()
    for (const path of paths) {
      const label = path === listed.default_shell
        ? `${path} (default)`
        : path
      await expect(page.getByRole('menuitemradio', { name: label, exact: true })).toBeVisible()
    }
  })

  test('runs the initial command of a terminal that the CLI opens', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    // The shell prints the product, which the command line does not hold, so
    // only a command that ran shows the line.
    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--command',
      'LEAPMUX_TEST_INITIAL_VALUE=$((6 * 7)); echo INITIAL_$LEAPMUX_TEST_INITIAL_VALUE',
    ]) as OpenedTerminal
    expect(opened.id).toBeTruthy()
    await expect(tabById(page, opened.id)).toBeVisible()

    const ran = await runCLI(cli, [
      'terminal',
      'wait',
      opened.id,
      '--regex',
      '^INITIAL_42$',
      '--timeout',
      '20s',
    ]) as TerminalWaitResult
    expect(ran).toMatchObject({ id: opened.id, reached: true, line: 'INITIAL_42' })

    // The next command reads state from the initial command in the same shell.
    await runCLI(cli, [
      'terminal',
      'run',
      opened.id,
      'echo AFTER_$LEAPMUX_TEST_INITIAL_VALUE',
    ])
    const after = await runCLI(cli, [
      'terminal',
      'wait',
      opened.id,
      '--regex',
      '^AFTER_42$',
      '--timeout',
      '20s',
    ]) as TerminalWaitResult
    expect(after).toMatchObject({ reached: true, line: 'AFTER_42' })
    const screen = await runCLI(cli, [
      'terminal',
      'read',
      opened.id,
      '--scrollback',
    ]) as RenderedScreen
    expect(screen.lines.filter(line => line === 'INITIAL_42'), 'the shell ran the initial command once').toHaveLength(1)

    // The browser shows the same output.
    await tabById(page, opened.id).click()
    await waitForTerminalText(page, 'INITIAL_42')
  })

  test('runs startup commands in the original Elvish shell', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    test.skip(process.platform === 'win32', 'The Elvish startup handoff requires a Unix Worker.')
    test.skip(spawnSync('elvish', ['-version'], { stdio: 'ignore' }).status !== 0, 'Elvish is unavailable on the test host.')

    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--shell',
      'elvish',
      '--command',
      'var leapmux-startup-test = (* 6 7); echo ELVISH_INITIAL_$leapmux-startup-test',
    ]) as OpenedTerminal
    await expect(tabById(page, opened.id)).toBeVisible()
    const wait = [
      'terminal',
      'wait',
      opened.id,
      '--timeout',
      '20s',
    ]
    expect(await runCLI(cli, [...wait, '--regex', '^ELVISH_INITIAL_42$'])).toMatchObject({
      reached: true,
      line: 'ELVISH_INITIAL_42',
    })
    await runCLI(cli, [
      'terminal',
      'run',
      opened.id,
      'echo ELVISH_AFTER_$leapmux-startup-test',
    ])
    expect(await runCLI(cli, [...wait, '--regex', '^ELVISH_AFTER_42$'])).toMatchObject({
      reached: true,
      line: 'ELVISH_AFTER_42',
    })
    await tabById(page, opened.id).click()
    await waitForTerminalText(page, 'ELVISH_AFTER_42')

    expect(await runCLI(cli, [
      'terminal',
      'restart',
      opened.id,
      '--replace',
      '--command',
      'exit 23',
    ])).toMatchObject({ id: opened.id })
    expect(await runCLI(cli, [...wait, '--exit'])).toMatchObject({
      reached: true,
      status: 'exited',
      exit_code: 23,
    })
  })

  test('keeps startup command state in the original Yash shell', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    test.skip(process.platform === 'win32', 'The Yash startup handoff requires a Unix Worker.')
    test.skip(spawnSync('yash', ['--version'], { stdio: 'ignore' }).status !== 0, 'Yash is unavailable on the test host.')

    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--shell',
      'yash',
      '--command',
      'leapmux_startup_value=$((6 * 7)); leapmux_startup_function() { printf "YASH_STATE_%s\\n" "$leapmux_startup_value"; }; leapmux_startup_function',
    ]) as OpenedTerminal
    const wait = [
      'terminal',
      'wait',
      opened.id,
      '--timeout',
      '20s',
    ]
    expect(await runCLI(cli, [...wait, '--regex', '^YASH_STATE_42$'])).toMatchObject({ reached: true, line: 'YASH_STATE_42' })
    await runCLI(cli, [
      'terminal',
      'run',
      opened.id,
      'leapmux_startup_value=$((leapmux_startup_value + 1)); leapmux_startup_function',
    ])
    expect(await runCLI(cli, [...wait, '--regex', '^YASH_STATE_43$'])).toMatchObject({ reached: true, line: 'YASH_STATE_43' })
    await expect(tabById(page, opened.id)).toBeVisible()
    await tabById(page, opened.id).click()
    await waitForTerminalText(page, 'YASH_STATE_43')
  })

  test('keeps startup command state in the original Xonsh shell', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    test.skip(process.platform === 'win32', 'The Xonsh startup handoff requires a Unix Worker.')
    test.skip(spawnSync('xonsh', ['--version'], { stdio: 'ignore' }).status !== 0, 'Xonsh is unavailable on the test host.')

    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--shell',
      'xonsh',
      '--command',
      'leapmux_value = 6 * 7\ndef leapmux_startup_function():\n    return leapmux_value\nprint("XONSH_STATE_" + str(leapmux_startup_function()))',
    ]) as OpenedTerminal
    const wait = [
      'terminal',
      'wait',
      opened.id,
      '--timeout',
      '20s',
    ]
    expect(await runCLI(cli, [...wait, '--regex', '^XONSH_STATE_42$'])).toMatchObject({ reached: true, line: 'XONSH_STATE_42' })
    await runCLI(cli, [
      'terminal',
      'run',
      opened.id,
      'leapmux_value += 1; print("XONSH_STATE_" + str(leapmux_startup_function()))',
    ])
    expect(await runCLI(cli, [...wait, '--regex', '^XONSH_STATE_43$'])).toMatchObject({ reached: true, line: 'XONSH_STATE_43' })
    await expect(tabById(page, opened.id)).toBeVisible()
    await tabById(page, opened.id).click()
    await waitForTerminalText(page, 'XONSH_STATE_43')
  })

  test('replaces the running shell in the same terminal tab', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--command',
      'export LEAPMUX_TEST_OLD_VALUE=$((6 * 7)); echo BEFORE_$LEAPMUX_TEST_OLD_VALUE',
    ]) as OpenedTerminal
    const waitForLine = async (regex: string) => {
      return await runCLI(cli, [
        'terminal',
        'wait',
        opened.id,
        '--regex',
        regex,
        '--timeout',
        '20s',
      ]) as TerminalWaitResult
    }
    expect(await waitForLine('^BEFORE_42$')).toMatchObject({ reached: true })
    await expect(runCLI(cli, ['terminal', 'restart', opened.id])).rejects.toThrow('terminal still running')

    expect(await runCLI(cli, [
      'terminal',
      'restart',
      opened.id,
      '--replace',
      '--command',
      'test -z "$LEAPMUX_TEST_OLD_VALUE" && LEAPMUX_TEST_NEW_VALUE=$((7 * 8)) && echo AFTER_$LEAPMUX_TEST_NEW_VALUE',
    ])).toMatchObject({ id: opened.id })
    expect(await waitForLine('^AFTER_56$')).toMatchObject({ reached: true })
    const screen = await runCLI(cli, [
      'terminal',
      'read',
      opened.id,
      '--scrollback',
    ]) as RenderedScreen
    expect(screen.lines.filter(line => line === 'BEFORE_42')).toHaveLength(1)
    expect(screen.lines.filter(line => line === 'AFTER_56')).toHaveLength(1)

    await tabById(page, opened.id).click()
    await waitForTerminalText(page, 'AFTER_56')
  })

  test('prints the exit code of a shell that ended before the wait', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--command',
      'exit 7',
    ]) as OpenedTerminal
    expect(opened.id).toBeTruthy()

    // The shell ends before the wait begins.
    await waitForTerminalExitViaAPI(leapmuxServer, authenticatedWorkspace.workspaceId, opened.id)
    await expect(tabById(page, opened.id), 'the tab knows that its shell exited').toHaveAttribute('data-terminal-status', String(TerminalStatus.EXITED))

    const waited = await runCLI(cli, [
      'terminal',
      'wait',
      opened.id,
      '--exit',
      '--timeout',
      '20s',
    ]) as TerminalWaitResult
    expect(waited).toEqual({
      id: opened.id,
      status: 'exited',
      reached: true,
      exit_code: 7,
    })

    const record = await runCLI(cli, ['terminal', 'get', opened.id]) as TerminalRecord
    expect(record).toMatchObject({ status: 'exited', exit_code: 7 })
  })
})
