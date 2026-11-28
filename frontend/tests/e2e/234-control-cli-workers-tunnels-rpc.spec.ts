/**
 * End-to-end coverage for worker, tunnel, events, and rpc CLI commands against
 * a live Hub, Workers, real TCP servers, and real SMTP capture:
 *
 *   worker list|get|info|ping|deregister|key create|key extend|key revoke|key email|pins list|pins get|pins remove,
 *   tunnel forward|connect,
 *   events,
 *   rpc.
 */

import type { AddressInfo } from 'node:net'
import { createConnection, createServer } from 'node:net'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { clearSmtpViaAPI, configureCaptureSmtpViaAPI, elevateSessionViaAPI, loginViaAPI, TEST_ADMIN_PASSWORD, waitForEmailEnabled } from './helpers/api'
import { withCleanup } from './helpers/cleanup'
import { mintCLITokenForAdmin, mintInteractiveOAuthCLI, runCLI, spawnCLILive, startControlEventStream } from './helpers/cli'
import { startCaptureSmtpServer } from './helpers/mail'
import { spawnRegisteredWorker } from './helpers/nativeWorker'
import { stopProcess } from './helpers/process'
import { createTestDirectory } from './helpers/runDirectory'
import { createServerOutput } from './helpers/serverOutput'

/** Start a loopback TCP echo server and return its bound port and stop function. */
async function startEchoServer(): Promise<{ port: number, stop: () => Promise<void> }> {
  const server = createServer((socket) => {
    socket.on('data', (chunk) => {
      socket.write(chunk)
    })
  })
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })
  const addr = server.address() as AddressInfo
  return {
    port: addr.port,
    stop: () => new Promise(resolve => server.close(() => resolve())),
  }
}

/** Find a free local TCP port for `tunnel forward`. */
async function pickFreePort(): Promise<number> {
  const server = createServer()
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })
  const port = (server.address() as AddressInfo).port
  await new Promise<void>(resolve => server.close(() => resolve()))
  return port
}

test.describe('control CLI workers, tunnels, events, and rpc', () => {
  test('inspects workers, pins, registration keys including email, and deregisters a secondary worker', async ({ leapmuxServer }) => {
    const cli = await mintInteractiveOAuthCLI(leapmuxServer)

    const listed = await runCLI(cli, ['worker', 'list']) as {
      workers: Array<{ id: string, online: boolean }>
    }
    expect(listed.workers.some(w => w.id === leapmuxServer.workerId && w.online)).toBe(true)

    const got = await runCLI(cli, ['worker', 'get', leapmuxServer.workerId]) as { id: string }
    expect(got.id).toBe(leapmuxServer.workerId)

    const info = await runCLI(cli, ['worker', 'info', leapmuxServer.workerId]) as {
      os?: string
      arch?: string
      hostname?: string
    }
    expect(info.os || info.hostname).toBeTruthy()

    const ping = await runCLI(cli, ['worker', 'ping', leapmuxServer.workerId]) as {
      ok?: boolean
      rtt_ms?: number
      latency_ms?: number
    }
    expect(ping).toBeTruthy()

    // TOFU pins populated by `worker info`/`worker ping` above.
    const pins = await runCLI(cli, ['worker', 'pins', 'list']) as {
      pins: Array<{ worker_id: string, fingerprint?: string }>
    }
    expect(pins.pins.some(p => p.worker_id === leapmuxServer.workerId)).toBe(true)

    const pinGot = await runCLI(cli, [
      'worker',
      'pins',
      'get',
      leapmuxServer.workerId,
    ]) as { worker_id: string }
    expect(pinGot.worker_id).toBe(leapmuxServer.workerId)

    await runCLI(cli, [
      'worker',
      'pins',
      'remove',
      leapmuxServer.workerId,
    ])
    const pinsAfterRemove = await runCLI(cli, ['worker', 'pins', 'list']) as {
      pins: Array<{ worker_id: string }>
    }
    expect(pinsAfterRemove.pins.some(p => p.worker_id === leapmuxServer.workerId)).toBe(false)

    // Registration keys: create, extend, email (via real capture SMTP), and revoke.
    // Use a disposable user created with a verified email so the hub's cached UserInfo
    // carries `email_verified=true` immediately and the shared admin account stays unmodified.
    const smtp = await startCaptureSmtpServer()
    await elevateSessionViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, TEST_ADMIN_PASSWORD)
    const mailUser = `e2e-wkmail-${Date.now()}`
    const mailPass = 'WorkerMail-E2E-2026!'
    const createdMailUser = await runCLI(cli, [
      'admin',
      'user',
      'create',
      mailUser,
      '--password',
      mailPass,
      '--email',
      'worker-mail-e2e@test.local',
      '--email-verified',
    ]) as { id: string }
    await withCleanup(async () => {
      await configureCaptureSmtpViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, smtp)
      await waitForEmailEnabled(leapmuxServer.hubUrl)
      const mailCookie = await loginViaAPI(leapmuxServer.hubUrl, mailUser, mailPass)
      const mailCli = await mintInteractiveOAuthCLI(
        { hubUrl: leapmuxServer.hubUrl, adminToken: mailCookie },
        {
          password: mailPass,
          scopes: ['account:read', 'worker:read', 'worker:admin'],
          elevate: false,
        },
      )

      const createdKey = await runCLI(mailCli, ['worker', 'key', 'create']) as { key: string }
      expect(createdKey.key).toBeTruthy()

      // Freshly minted keys are not yet in the extension window; confirm the real
      // Hub enforces that guard through `worker key extend`.
      await expect(runCLI(mailCli, [
        'worker',
        'key',
        'extend',
        createdKey.key,
      ])).rejects.toMatchObject({ code: 'conflict' })

      const emailWait = smtp.waitForMessage()
      const emailed = await runCLI(mailCli, [
        'worker',
        'key',
        'email',
        createdKey.key,
      ]) as { sent: boolean }
      expect(emailed.sent).toBe(true)
      const receivedMail = await emailWait
      expect(receivedMail).toContain(createdKey.key)

      await runCLI(mailCli, [
        'worker',
        'key',
        'revoke',
        createdKey.key,
      ])
    }, async () => {
      await runCLI(cli, [
        'admin',
        'user',
        'delete',
        createdMailUser.id,
        '--force',
      ]).catch(() => {})
      await clearSmtpViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken).catch(() => {})
      await smtp.stop()
    })

    // Register a disposable 2nd worker and deregister it via `worker deregister`.
    const extraDataDir = createTestDirectory('leapmux-extra-worker-')
    const extra = await spawnRegisteredWorker({
      hubUrl: leapmuxServer.hubUrl,
      adminToken: leapmuxServer.adminToken,
    }, {
      name: 'cli-deregister-target',
      dataDir: extraDataDir,
      env: leapmuxServer.agentEnv,
      output: createServerOutput(),
    })
    try {
      await runCLI(cli, ['worker', 'deregister', extra.workerId])
      const afterDereg = await runCLI(cli, ['worker', 'list']) as {
        workers: Array<{ id: string }>
      }
      expect(afterDereg.workers.some(w => w.id === extra.workerId)).toBe(false)
    }
    finally {
      await stopProcess(extra.proc)
    }
  })

  test('forwards TCP ports and pipes stdio through E2EE tunnels plus exercises events and rpc', async ({ authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const echo = await startEchoServer()
    try {
      // 1. `tunnel connect` stdio round-trip over Noise_NK E2EE.
      const connProc = spawnCLILive(cli, [
        'tunnel',
        'connect',
        `127.0.0.1:${echo.port}`,
        '--worker',
        leapmuxServer.workerId,
      ])
      try {
        connProc.child.stdin?.write('TUNNEL_STDIO_HELLO\n')
        await expect.poll(() => connProc.stdout()).toContain('TUNNEL_STDIO_HELLO')
      }
      finally {
        await connProc.stop()
      }

      // 2. `tunnel forward` TCP round-trip over Noise_NK E2EE.
      const localPort = await pickFreePort()
      const fwdProc = spawnCLILive(cli, [
        'tunnel',
        'forward',
        `${localPort}:127.0.0.1:${echo.port}`,
        '--worker',
        leapmuxServer.workerId,
      ], { json: true })
      try {
        await expect.poll(() => fwdProc.stdout() + fwdProc.stderr()).toContain(String(localPort))
        const echoed = await new Promise<string>((resolve, reject) => {
          const sock = createConnection({ host: '127.0.0.1', port: localPort }, () => {
            sock.write('TUNNEL_FORWARD_PING\n')
          })
          let buf = ''
          sock.on('data', (chunk) => {
            buf += chunk.toString('utf8')
            if (buf.includes('TUNNEL_FORWARD_PING')) {
              sock.end()
              resolve(buf)
            }
          })
          sock.once('error', reject)
        })
        expect(echoed).toContain('TUNNEL_FORWARD_PING')
      }
      finally {
        await fwdProc.stop()
      }
    }
    finally {
      await echo.stop()
    }

    // 3. `rpc` against both the Hub and a Worker inner procedure.
    const hubRpc = await runCLI(cli, [
      'rpc',
      'WorkspaceService/ListWorkspaces',
      '{}',
    ]) as { workspaces: Array<{ id: string }> }
    expect(hubRpc.workspaces.some(w => w.id === authenticatedWorkspace.workspaceId)).toBe(true)

    const workerRpc = await runCLI(cli, [
      'rpc',
      'WorkerTerminalService/ListAvailableShells',
      '{}',
      '--worker',
      leapmuxServer.workerId,
    ]) as { shells: string[] }
    expect(workerRpc.shells.length).toBeGreaterThan(0)

    // 4. `events` live stream receives workspace updates cleanly.
    const stream = startControlEventStream(cli, [
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--kind',
      'stream.ready',
    ])
    try {
      await expect.poll(() => stream.events().map(e => e.kind)).toContain('stream.ready')
    }
    finally {
      stream.child.kill('SIGINT')
      await stream.finished
    }
  })
})
