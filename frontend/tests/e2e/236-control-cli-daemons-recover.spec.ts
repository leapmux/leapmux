/**
 * End-to-end coverage for all 48 `daemon`, `up`, `down`, and `recover` CLI leaf
 * commands against real isolated `--data-dir` instances and a live Hub:
 *
 *   recover create-admin|reset-password|db path|db version|db migrate,
 *   recover encryption-key rotate|reencrypt|remove|rotate-pepper,
 *   daemon hub run|status|logs|stop|disable|enable|start|restart,
 *   daemon solo run|status|logs|stop|disable|enable|start|restart,
 *   daemon dev run|status|logs|stop|disable|enable|start|restart,
 *   daemon worker register|run|status|logs|stop|deregister|disable|enable|start|restart,
 *   daemon worker cross-worker-pins list|get|remove,
 *   up, down.
 *
 * Native OS service installation (`enable|start|restart|up`) is exercised via
 * the non-interactive consent guard (`LEAPMUX_CONTROL_NO_PROMPT=1` without
 * `--yes`) and `disable` against unconfigured isolated temp directories so this
 * spec never mutates host launchd or systemd units.
 */

import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { loginViaAPI } from './helpers/api'
import { CLIError, mintCLITokenForAdmin, runCLI, runCLILocal } from './helpers/cli'
import { stopProcess } from './helpers/process'
import { spawnTestProcess } from './helpers/processRegistry'
import { createTestDirectory } from './helpers/runDirectory'
import { getGlobalState, hubSpawnEnv, waitForHubStart } from './helpers/server'

/** Wait for a spawned child process to exit and return its exit code or signal. */
function waitForExit(proc: ReturnType<typeof spawnTestProcess>): Promise<{ code: number | null, signal: NodeJS.Signals | null }> {
  if (proc.exitCode !== null || proc.signalCode !== null)
    return Promise.resolve({ code: proc.exitCode, signal: proc.signalCode })
  return new Promise((resolve) => {
    proc.once('exit', (code, signal) => resolve({ code, signal }))
  })
}

/**
 * Exercise the non-mutating native service verbs (`disable`, `enable`, `start`,
 * `restart`) for a daemon mode against an isolated unconfigured data directory.
 */
async function exerciseNonMutatingServiceVerbs(mode: 'hub' | 'solo' | 'dev' | 'worker', dataDir: string): Promise<void> {
  const noPrompt = { LEAPMUX_CONTROL_NO_PROMPT: '1' }
  const disabled = await runCLILocal([
    'daemon',
    mode,
    'disable',
    '--data-dir',
    dataDir,
  ], { env: noPrompt }) as { status?: string, mode?: string }
  expect(disabled).toBeTruthy()

  for (const verb of ['enable', 'start', 'restart'] as const) {
    await expect(runCLILocal([
      'daemon',
      mode,
      verb,
      '--data-dir',
      dataDir,
    ], { env: noPrompt })).rejects.toMatchObject({ code: 'usage' })
  }
}

test.describe('control CLI daemons and recover commands', () => {
  test('runs hub lifecycle and all nine offline recover commands on an isolated database', async () => {
    const { binaryPath } = getGlobalState()
    const hubDir = createTestDirectory('leapmux-cli-hub-rec-')
    const hubProc = spawnTestProcess(binaryPath, [
      'daemon',
      'hub',
      'run',
      '--listen',
      '127.0.0.1:0',
      '--data-dir',
      hubDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv(),
    })

    try {
      await waitForHubStart(join(hubDir, 'state.json'), hubProc)

      // Live `daemon hub status`, `logs`, and graceful `stop`.
      const status = await runCLILocal([
        'daemon',
        'hub',
        'status',
        '--data-dir',
        hubDir,
      ]) as { process?: { state?: string }, mode?: string }
      expect(status.mode).toBe('hub')
      expect(status.process?.state).toBe('running')

      const hubLogs = await runCLILocal([
        'daemon',
        'hub',
        'logs',
        '--data-dir',
        hubDir,
        '--lines',
        '10',
      ]) as { mode: string, path: string, lines: string[] }
      expect(hubLogs.mode).toBe('hub')
      expect(hubLogs.lines.length).toBeGreaterThan(0)

      const exitPromise = waitForExit(hubProc)
      await runCLILocal([
        'daemon',
        'hub',
        'stop',
        '--data-dir',
        hubDir,
      ])
      const exited = await exitPromise
      expect(exited.code).toBe(0)
    }
    finally {
      await stopProcess(hubProc)
    }

    await exerciseNonMutatingServiceVerbs('hub', hubDir)

    // Offline `recover` suite (all 9 leaf commands) against the stopped Hub data dir.
    const dbPath = await runCLILocal([
      'recover',
      'db',
      'path',
      '--data-dir',
      hubDir,
    ]) as { path: string }
    expect(dbPath.path).toContain(hubDir)
    expect(existsSync(dbPath.path)).toBe(true)

    const dbVer = await runCLILocal([
      'recover',
      'db',
      'version',
      '--data-dir',
      hubDir,
    ]) as { current_version: number, latest_version: number }
    expect(dbVer.current_version).toBeGreaterThan(0)
    expect(dbVer.current_version).toBe(dbVer.latest_version)

    const dbMig = await runCLILocal([
      'recover',
      'db',
      'migrate',
      '--data-dir',
      hubDir,
    ]) as { already_latest: boolean, current_version: number }
    expect(dbMig.already_latest).toBe(true)
    expect(dbMig.current_version).toBe(dbVer.latest_version)

    const createdAdmin = await runCLILocal([
      'recover',
      'create-admin',
      'breakglass',
      '--password',
      'BreakGlass-Init-2026!',
      '--display-name',
      'Break Glass Admin',
      '--data-dir',
      hubDir,
    ]) as { id: string, username: string, is_admin: boolean }
    expect(createdAdmin.username).toBe('breakglass')
    expect(createdAdmin.is_admin).toBe(true)

    // Second `recover create-admin` must refuse with `conflict`.
    await expect(runCLILocal([
      'recover',
      'create-admin',
      'secondadmin',
      '--password',
      'Second-Admin-2026!',
      '--data-dir',
      hubDir,
    ])).rejects.toMatchObject({ code: 'conflict' })

    const newPass = 'BreakGlass-Reset-2026!'
    const resetPw = await runCLILocal([
      'recover',
      'reset-password',
      'breakglass',
      '--password',
      newPass,
      '--data-dir',
      hubDir,
    ]) as { id: string, sessions_revoked: boolean, passkeys_revoked: boolean }
    expect(resetPw.id).toBe(createdAdmin.id)
    expect(resetPw.sessions_revoked).toBe(true)
    expect(resetPw.passkeys_revoked).toBe(true)

    const rotated = await runCLILocal([
      'recover',
      'encryption-key',
      'rotate',
      '--data-dir',
      hubDir,
    ]) as { version: number }
    expect(rotated.version).toBe(2)

    const reencrypted = await runCLILocal([
      'recover',
      'encryption-key',
      'reencrypt',
      '--data-dir',
      hubDir,
    ]) as { active_version: number, rows_updated: number }
    expect(reencrypted.active_version).toBe(2)

    const removedKey = await runCLILocal([
      'recover',
      'encryption-key',
      'remove',
      '1',
      '--data-dir',
      hubDir,
    ]) as { removed_version: number }
    expect(removedKey.removed_version).toBe(1)

    // `rotate-pepper` must refuse without `--yes`, then succeed with `--yes`.
    await expect(runCLILocal([
      'recover',
      'encryption-key',
      'rotate-pepper',
      '--data-dir',
      hubDir,
    ])).rejects.toMatchObject({ code: 'usage' })

    const rotatedPepper = await runCLILocal([
      'recover',
      'encryption-key',
      'rotate-pepper',
      '--yes',
      '--data-dir',
      hubDir,
    ]) as { rotated: boolean }
    expect(rotatedPepper.rotated).toBe(true)

    // Restart the Hub to verify the recovered DB, rotated keys, and reset password work live.
    const restartedHub = spawnTestProcess(binaryPath, [
      'daemon',
      'hub',
      'run',
      '--listen',
      '127.0.0.1:0',
      '--data-dir',
      hubDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv(),
    })
    try {
      const started = await waitForHubStart(join(hubDir, 'state.json'), restartedHub)
      const cookie = await loginViaAPI(started.hubUrl, 'breakglass', newPass)
      expect(cookie).toBeTruthy()
    }
    finally {
      await stopProcess(restartedHub)
    }
  })

  test('runs solo, dev, up, and down daemon lifecycles on isolated directories', async () => {
    const { binaryPath } = getGlobalState()

    // 1. `daemon solo` + `up` + `down`
    const soloDir = createTestDirectory('leapmux-cli-solo-')
    const soloProc = spawnTestProcess(binaryPath, [
      'daemon',
      'solo',
      'run',
      '--listen',
      '127.0.0.1:0',
      '--data-dir',
      soloDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv(),
    })
    try {
      await expect.poll(async () => {
        try {
          const soloStatus = await runCLILocal([
            'daemon',
            'solo',
            'status',
            '--data-dir',
            soloDir,
          ]) as { mode?: string, process?: { state?: string } }
          return `${soloStatus.mode}:${soloStatus.process?.state}`
        }
        catch {
          return ''
        }
      }).toBe('solo:running')

      const soloLogs = await runCLILocal([
        'daemon',
        'solo',
        'logs',
        '--data-dir',
        soloDir,
        '--lines',
        '10',
      ]) as { mode: string, lines: string[] }
      expect(soloLogs.mode).toBe('solo')
      expect(soloLogs.lines.length).toBeGreaterThan(0)

      // Stop via top-level `down` alias for `daemon solo stop`.
      const soloExit = waitForExit(soloProc)
      await runCLILocal(['down', '--data-dir', soloDir])
      expect((await soloExit).code).toBe(0)

      // Calling `daemon solo stop` when already stopped completes cleanly.
      await runCLILocal([
        'daemon',
        'solo',
        'stop',
        '--data-dir',
        soloDir,
      ])
    }
    finally {
      await stopProcess(soloProc)
    }

    await exerciseNonMutatingServiceVerbs('solo', soloDir)
    await expect(runCLILocal([
      'up',
      '--data-dir',
      soloDir,
    ], {
      env: { LEAPMUX_CONTROL_NO_PROMPT: '1' },
    })).rejects.toMatchObject({ code: 'usage' })

    // 2. `daemon dev`
    const devDir = createTestDirectory('leapmux-cli-dev-')
    const devProc = spawnTestProcess(binaryPath, [
      'daemon',
      'dev',
      'run',
      '--listen',
      '127.0.0.1:0',
      '--data-dir',
      devDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv(),
    })
    try {
      await expect.poll(async () => {
        try {
          const devStatus = await runCLILocal([
            'daemon',
            'dev',
            'status',
            '--data-dir',
            devDir,
          ]) as { mode?: string, process?: { state?: string } }
          return `${devStatus.mode}:${devStatus.process?.state}`
        }
        catch {
          return ''
        }
      }).toBe('dev:running')

      const devLogs = await runCLILocal([
        'daemon',
        'dev',
        'logs',
        '--data-dir',
        devDir,
        '--lines',
        '10',
      ]) as { mode: string, lines: string[] }
      expect(devLogs.mode).toBe('dev')
      expect(devLogs.lines.length).toBeGreaterThan(0)

      const devExit = waitForExit(devProc)
      await runCLILocal([
        'daemon',
        'dev',
        'stop',
        '--data-dir',
        devDir,
      ])
      expect((await devExit).code).toBe(0)
    }
    finally {
      await stopProcess(devProc)
    }

    await exerciseNonMutatingServiceVerbs('dev', devDir)
  })

  test('registers, runs, pins a sibling worker, stops, and deregisters a standalone worker daemon', async ({ leapmuxServer }) => {
    const { binaryPath } = getGlobalState()
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const workerDir = createTestDirectory('leapmux-cli-worker-daemon-')

    const regKey = await runCLI(cli, ['worker', 'key', 'create']) as { key: string }
    expect(regKey.key).toBeTruthy()

    // 1. `daemon worker register`
    await runCLILocal([
      'daemon',
      'worker',
      'register',
      '--hub',
      leapmuxServer.hubUrl,
      '--registration-key',
      regKey.key,
      '--data-dir',
      workerDir,
    ])
    const workerState = JSON.parse(readFileSync(join(workerDir, 'state.json'), 'utf8')) as {
      worker_id: string
    }
    const newWorkerId = workerState.worker_id
    expect(newWorkerId).toBeTruthy()

    // 2. `daemon worker run`, `status`, `logs`, real cross-worker pin, and `stop`.
    const workerProc = spawnTestProcess(binaryPath, [
      'daemon',
      'worker',
      'run',
      '--data-dir',
      workerDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv(),
    })
    let wsId: string | null = null
    try {
      await expect.poll(async () => {
        const listed = await runCLI(cli, ['worker', 'list']) as {
          workers: Array<{ id: string, online: boolean }>
        }
        return listed.workers.some(w => w.id === newWorkerId && w.online)
      }).toBe(true)

      const workerStatus = await runCLILocal([
        'daemon',
        'worker',
        'status',
        '--data-dir',
        workerDir,
      ]) as { mode?: string, process?: { state?: string } }
      expect(workerStatus.mode).toBe('worker')
      expect(workerStatus.process?.state).toBe('running')

      const workerLogs = await runCLILocal([
        'daemon',
        'worker',
        'logs',
        '--data-dir',
        workerDir,
        '--lines',
        '10',
      ]) as { mode: string, lines: string[] }
      expect(workerLogs.mode).toBe('worker')
      expect(workerLogs.lines.length).toBeGreaterThan(0)

      // Trigger a real cross-worker E2EE connection from the new worker to the
      // primary sibling worker so `cross_worker_pins.json` is populated naturally.
      const ws = await runCLI(cli, ['workspace', 'create', 'Cross Worker Pin E2E']) as { id: string }
      wsId = ws.id
      const term = await runCLI(cli, [
        'terminal',
        'open',
        '--workspace',
        ws.id,
        '--worker',
        newWorkerId,
      ]) as { id: string }
      await runCLI(cli, [
        'terminal',
        'run',
        term.id,
        `${binaryPath} worker ping ${leapmuxServer.workerId} --json`,
      ])

      await expect.poll(async () => {
        const pins = await runCLILocal([
          'daemon',
          'worker',
          'cross-worker-pins',
          'list',
          '--data-dir',
          workerDir,
        ]) as { pins: Array<{ worker_id: string }> }
        return pins.pins.some(p => p.worker_id === leapmuxServer.workerId)
      }).toBe(true)

      const pinGot = await runCLILocal([
        'daemon',
        'worker',
        'cross-worker-pins',
        'get',
        leapmuxServer.workerId,
        '--data-dir',
        workerDir,
      ]) as { worker_id: string, x25519_public_key: string }
      expect(pinGot.worker_id).toBe(leapmuxServer.workerId)
      expect(pinGot.x25519_public_key).toBeTruthy()

      const pinRemoved = await runCLILocal([
        'daemon',
        'worker',
        'cross-worker-pins',
        'remove',
        leapmuxServer.workerId,
        '--data-dir',
        workerDir,
      ]) as { removed: string }
      expect(pinRemoved.removed).toBe(leapmuxServer.workerId)

      await expect(runCLILocal([
        'daemon',
        'worker',
        'cross-worker-pins',
        'get',
        leapmuxServer.workerId,
        '--data-dir',
        workerDir,
      ])).rejects.toBeInstanceOf(CLIError)

      const workerExit = waitForExit(workerProc)
      await runCLILocal([
        'daemon',
        'worker',
        'stop',
        '--data-dir',
        workerDir,
      ])
      expect((await workerExit).code).toBe(0)
    }
    finally {
      await stopProcess(workerProc)
      if (wsId)
        await runCLI(cli, ['workspace', 'delete', wsId]).catch(() => {})
    }

    // Exercise non-mutating native service verbs while the worker is registered and stopped.
    await exerciseNonMutatingServiceVerbs('worker', workerDir)

    // 3. `daemon worker deregister` removes the worker from Hub and deletes local state.json.
    const dereg = await runCLILocal([
      'daemon',
      'worker',
      'deregister',
      '--data-dir',
      workerDir,
    ]) as { status: string, worker_id: string }
    expect(dereg.status).toBe('deregistered')
    expect(dereg.worker_id).toBe(newWorkerId)
    expect(existsSync(join(workerDir, 'state.json'))).toBe(false)
  })
})
