/**
 * End-to-end coverage of the terminal commands of `leapmux control` that a
 * real shell decides: `terminal open --command` and `terminal wait --exit`.
 *
 * Go tests cover the request, the readiness rule and the exit-code record with
 * a recording shell and fake Workers. This spec runs the CLI against the real
 * Hub, Worker and login shell, and observes the result in the CLI and in the
 * browser.
 *
 * Each `terminal wait` passes a `--timeout` below the 30-second limit of
 * `runCLI`, so a wait that does not end fails with the CLI's own message.
 */

import { expect } from '@playwright/test'
import { TerminalStatus } from '../../src/generated/proto/leapmux/v1/terminal_pb'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { waitForTerminalText } from './helpers/terminal'
import { tabById } from './helpers/ui'
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
      'echo INITIAL_$((6 * 7))',
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

    // The shell runs its input in order, so a second copy of the command
    // that reached it before this command shows before AFTER_2.
    await runCLI(cli, [
      'terminal',
      'run',
      opened.id,
      'echo AFTER_$((1 + 1))',
    ])
    const after = await runCLI(cli, [
      'terminal',
      'wait',
      opened.id,
      '--regex',
      '^AFTER_2$',
      '--timeout',
      '20s',
    ]) as TerminalWaitResult
    expect(after).toMatchObject({ reached: true })
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
