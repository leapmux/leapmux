import type { ChildProcess } from 'node:child_process'
import { copyFileSync, existsSync, rmSync } from 'node:fs'
import { delimiter, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { spawnWindowsCommandJob } from '../../scripts/windowsCommandJob'
import { test } from './fixtures'
import { deregisterWorkerViaAPI, listWorkersViaAPI, waitForNewOnlineWorkerViaAPI } from './helpers/api'
import { finishCleanup, withCleanup } from './helpers/cleanup'
import { captureClipboardText } from './helpers/clipboardCapture'
import { spawnWorkerProcess } from './helpers/nativeWorker'
import { stopProcess } from './helpers/process'
import { spawnTestProcess, trackProcess } from './helpers/processRegistry'
import { createTestDirectory } from './helpers/runDirectory'
import { getGlobalState, hubSpawnEnv } from './helpers/server'
import { createServerOutput } from './helpers/serverOutput'
import { expandSidebarSection, openAppAs, sidebarSectionHeader } from './helpers/ui'

// The registration dialog's two-step CLI: the registration command and
// the start command are SEPARATE commands with separate copy actions,
// the shell pills switch the quoting, and the register command never
// starts the worker by itself.
test.describe('registration dialog CLI', () => {
  test('executes copied registration to completion before a separate worker comes online', async ({ page, leapmuxServer }) => {
    await openAppAs(page, leapmuxServer.adminToken)
    const section = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(section)
    const previous = new Set((await listWorkersViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)).map(worker => worker.id))
    await page.getByTestId('sidebar-register-worker').click()
    const windows = process.platform === 'win32'
    if (windows)
      await page.getByRole('radio', { name: 'PowerShell' }).click()
    const copied = await captureClipboardText(page, () => page.getByTestId('copy-registration-command').click())
    expect(copied).toBe(await page.getByTestId('registration-command').textContent())
    const start = await captureClipboardText(page, () => page.getByTestId('copy-start-command').click())
    expect(start).toBe('leapmux worker start')
    const directory = createTestDirectory('copied-registration-')
    const binaryDirectory = createTestDirectory('copied-registration-bin-')
    const command = copied
    const output = createServerOutput()
    let registration: ChildProcess | undefined
    let registrationJob: ReturnType<typeof spawnWindowsCommandJob> | undefined
    let runtime: ChildProcess | undefined
    let workerId: string | undefined
    await withCleanup(async () => {
      copyFileSync(getGlobalState().binaryPath, join(binaryDirectory, windows ? 'leapmux.exe' : 'leapmux'))
      const environment = hubSpawnEnv(leapmuxServer.agentEnv)
      const pathKey = Object.keys(environment).find(key => key.toUpperCase() === 'PATH') ?? 'PATH'
      environment[pathKey] = `${binaryDirectory}${delimiter}${environment[pathKey] ?? ''}`
      environment.LEAPMUX_WORKER_DATA_DIR = directory
      const options = {
        stdio: ['ignore', 'pipe', 'pipe'] as const,
        env: environment,
      }
      if (windows) {
        registrationJob = spawnWindowsCommandJob('powershell.exe', [
          '-NoProfile',
          '-NonInteractive',
          '-Command',
          command,
        ], {
          ...options,
          stdio: [...options.stdio],
        }, 5000)
        registration = registrationJob.child
        trackProcess(getGlobalState().tmpDir, registration)
      }
      else {
        registration = spawnTestProcess('/bin/sh', ['-c', command], {
          ...options,
          stdio: [...options.stdio],
        })
      }
      output.capture(registration, 'copied registration')
      const result = await new Promise<{ code: number | null, signal: NodeJS.Signals | null }>((resolve, reject) => {
        registration!.once('error', reject)
        registration!.once('close', (code, signal) => resolve({ code, signal }))
      })
      expect(result, output.since(0)).toEqual({ code: 0, signal: null })
      expect(existsSync(join(directory, 'state.json'))).toBe(true)
      expect(existsSync(join(directory, 'worker.db'))).toBe(false)
      const registered = (await listWorkersViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)).filter(worker => !previous.has(worker.id))
      expect(registered).toHaveLength(1)
      workerId = registered[0]!.id
      expect(registered[0]!.online).toBe(false)
      runtime = spawnWorkerProcess({
        hubUrl: leapmuxServer.hubUrl,
        name: 'copied-command-worker',
        dataDir: directory,
        env: leapmuxServer.agentEnv,
        output,
      })
      expect(await waitForNewOnlineWorkerViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, previous)).toBe(workerId)
      await page.getByRole('dialog', { name: 'Register worker' }).getByRole('button', { name: 'Close', exact: true }).click()
      await expect(section.locator('[data-testid="worker-row"]:visible').filter({ hasText: 'copied-command-worker' }).first()).toBeVisible()
    }, async () => {
      await finishCleanup([
        runtime
          ? stopProcess(runtime)
          : Promise.resolve(),
        registrationJob
          ? registrationJob.stop()
          : registration
            ? stopProcess(registration)
            : Promise.resolve(),
        workerId
          ? deregisterWorkerViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, workerId)
          : Promise.resolve(),
      ])
      rmSync(directory, { recursive: true, force: true })
      rmSync(binaryDirectory, { recursive: true, force: true })
    })
  })
  test('renders register then start commands for both shells', async ({ page, leapmuxServer }) => {
    await openAppAs(page, leapmuxServer.adminToken)
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)
    await page.getByTestId('sidebar-register-worker').click()

    const command = page.getByTestId('registration-command')
    await expect(command).toContainText('leapmux worker register --hub')
    await expect(command).toContainText('--registration-key')
    await expect(page.getByTestId('start-command')).toHaveText('leapmux worker start')

    // Registration and startup stay separate: the register command
    // never starts the worker by itself.
    await expect(command).not.toContainText(' start')

    // The shell pills switch the quoting.
    await page.getByRole('radio', { name: 'PowerShell' }).click()
    await expect(command).toContainText('System.Diagnostics.ProcessStartInfo')
    await expect(command).toContainText('--hub "')
  })

  test('offers separate copy actions and keeps the key valid while open', async ({ page, leapmuxServer }) => {
    await openAppAs(page, leapmuxServer.adminToken)
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)
    await page.getByTestId('sidebar-register-worker').click()

    await expect(page.getByTestId('copy-registration-command')).toBeEnabled()
    await expect(page.getByTestId('copy-start-command')).toBeEnabled()
    await expect(commandLocator(page)).toContainText('--registration-key')
  })
})

function commandLocator(page: import('@playwright/test').Page) {
  return page.getByTestId('registration-command')
}
