/**
 * End-to-end coverage for settings, auth, and skill CLI commands against a live
 * Hub and Worker:
 *
 *   settings list|get|set|reset,
 *   auth login|status|list|use|credentials|change-password|logout,
 *   skill list|show|status|diff|install|uninstall.
 */

import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { elevateSessionViaAPI, loginViaAPI } from './helpers/api'
import { approveOAuthDeviceCode, CLIError, elevateCLICredential, mintCLITokenForAdmin, mintInteractiveOAuthCLI, readCLICredentialToken, runCLI, runCLILocal, runCLILocalText, spawnCLILive } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'

test.describe('control CLI settings, auth, and skills', () => {
  test('lists, gets, sets, and resets user settings on the live hub', async ({ leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    const all = await runCLI(cli, ['settings', 'list']) as {
      settings: Array<{ key: string, value?: unknown }>
    }
    expect(all.settings.length).toBeGreaterThan(0)

    await runCLI(cli, [
      'settings',
      'set',
      'theme',
      '{"mode":"dark"}',
    ])
    const got = await runCLI(cli, [
      'settings',
      'get',
      'theme',
    ]) as { key: string, customized: boolean, effective_json: unknown }
    expect(got.key).toBe('theme')
    expect(got.customized).toBe(true)
    expect(JSON.stringify(got.effective_json)).toContain('dark')

    await runCLI(cli, [
      'settings',
      'reset',
      'theme',
    ])
    const afterReset = await runCLI(cli, [
      'settings',
      'get',
      'theme',
    ]) as { key: string, customized: boolean }
    expect(afterReset.key).toBe('theme')
    expect(afterReset.customized).toBe(false)
  })

  test('completes real OAuth device-code login, credential inspection, password change, and logout', async ({ leapmuxServer }) => {
    const adminCli = await mintInteractiveOAuthCLI(leapmuxServer)
    const disposableUser = `e2e-auth-${Date.now()}`
    const initialPass = 'Initial-E2E-Pass-2026!'
    const nextPass = 'Updated-E2E-Pass-2026!'

    const created = await runCLI(adminCli, [
      'admin',
      'user',
      'create',
      disposableUser,
      '--password',
      initialPass,
      '--display-name',
      'CLI Auth Test',
    ]) as { id: string }

    try {
      const userCookie = await loginViaAPI(leapmuxServer.hubUrl, disposableUser, initialPass)
      await elevateSessionViaAPI(leapmuxServer.hubUrl, userCookie, initialPass)

      // 1. Real `leapmux auth login --device-code` approved via `/oauth/device`.
      const authConfigDir = createTestDirectory('leapmux-auth-e2e-')
      const loginProc = spawnCLILive(null, [
        'auth',
        'login',
        '--hub',
        leapmuxServer.hubUrl,
        '--device-code',
        '--device-name',
        'e2e-device',
      ], {
        env: { LEAPMUX_CONTROL_CONFIG_DIR: authConfigDir },
      })

      try {
        await expect.poll(() => `${loginProc.stdout()}\n${loginProc.stderr()}`).toMatch(/user_code=[A-Z0-9-]+/)
        const combined = `${loginProc.stdout()}\n${loginProc.stderr()}`
        const codeMatch = /user_code=([A-Z0-9-]+)/.exec(combined)
        expect(codeMatch?.[1]).toBeTruthy()
        await approveOAuthDeviceCode(leapmuxServer.hubUrl, userCookie, codeMatch![1]!)
        const exit = await loginProc.finished
        expect(exit.code, loginProc.diagnostic()).toBe(0)
      }
      finally {
        await loginProc.stop()
      }

      const userCli = {
        path: authConfigDir,
        hubURL: leapmuxServer.hubUrl,
        bearer: '',
        userID: created.id,
      }

      // 2. `auth list`, `auth use`, `auth status`, `auth credentials`.
      const hubs = await runCLILocal(['auth', 'list'], { configDir: authConfigDir }) as {
        credentials: Array<{ hub_url: string }>
      }
      expect(hubs.credentials.some(h => h.hub_url === leapmuxServer.hubUrl)).toBe(true)

      await runCLILocal([
        'auth',
        'use',
        leapmuxServer.hubUrl,
      ], { configDir: authConfigDir })

      const status = await runCLI(userCli, ['auth', 'status']) as {
        hub_url: string
        username?: string
      }
      expect(status.hub_url).toBe(leapmuxServer.hubUrl)

      const creds = await runCLI(userCli, ['auth', 'credentials']) as {
        credentials: Array<{ installation_name?: string }>
      }
      expect(creds.credentials.length).toBeGreaterThan(0)

      // 3. `auth change-password` on the disposable user and confirm the new password signs in.
      const deviceStatus = await runCLI(userCli, ['whoami']) as { username: string }
      expect(deviceStatus.username).toBe(disposableUser)

      // Elevate the freshly logged-in CLI credential via `/oauth/step-up` + `/oauth/device`,
      // then run `auth change-password`.
      const accessToken = readCLICredentialToken(authConfigDir, leapmuxServer.hubUrl)
      await elevateCLICredential(leapmuxServer.hubUrl, accessToken, userCookie)
      const changed = await runCLI(userCli, [
        'auth',
        'change-password',
        '--password',
        nextPass,
      ]) as { changed: boolean, username: string }
      expect(changed.changed).toBe(true)
      expect(changed.username).toBe(disposableUser)

      // Prove the new password logs in cleanly.
      const newCookie = await loginViaAPI(leapmuxServer.hubUrl, disposableUser, nextPass)
      expect(newCookie).toBeTruthy()

      // 4. `auth logout` revokes and removes the local credential.
      await runCLI(userCli, ['auth', 'logout'])
      await expect(runCLI(userCli, ['whoami'])).rejects.toBeInstanceOf(CLIError)
    }
    finally {
      await runCLI(adminCli, [
        'admin',
        'user',
        'delete',
        created.id,
        '--force',
      ]).catch(() => {})
    }
  })

  test('lists, shows, installs, checks status, diffs, and uninstalls skills locally and on a worker', async ({ leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const projectDir = createTestDirectory('leapmux-cli-skills-')
    mkdirSync(join(projectDir, '.claude', 'skills'), { recursive: true })

    // Local skill workflow (project scope inside temp directory).
    const listed = await runCLILocal(['skill', 'list'], { cwd: projectDir }) as {
      skills: Array<{ name: string }>
    }
    expect(listed.skills.length).toBeGreaterThanOrEqual(6)

    const shown = await runCLILocalText(['skill', 'show', 'leapmux-delegate'])
    expect(shown.stdout).toContain('leapmux')

    await runCLILocal([
      'skill',
      'install',
      'leapmux-delegate',
      '--scope',
      'project',
      '--target',
      'claude',
    ], { cwd: projectDir })
    const installedSkillMd = join(projectDir, '.claude', 'skills', 'leapmux-delegate', 'SKILL.md')
    expect(existsSync(installedSkillMd)).toBe(true)

    const status = await runCLILocal([
      'skill',
      'status',
      '--scope',
      'project',
      '--target',
      'claude',
    ], { cwd: projectDir }) as { entries?: unknown[], targets?: unknown[] }
    expect(status).toBeTruthy()

    // Modify installed SKILL.md to verify `skill diff` reports the real unified diff.
    writeFileSync(installedSkillMd, '# Local Drift\n')
    const diff = await runCLILocal([
      'skill',
      'diff',
      'leapmux-delegate',
      '--scope',
      'project',
      '--target',
      'claude',
    ], { cwd: projectDir }) as { diffs: Array<{ files: Array<{ diff?: string }> }> }
    const allFiles = diff.diffs.flatMap(d => d.files ?? [])
    expect(allFiles.some(f => (f.diff ?? '').includes('Local Drift'))).toBe(true)

    await runCLILocal([
      'skill',
      'uninstall',
      'leapmux-delegate',
      '--scope',
      'project',
      '--target',
      'claude',
      '--force',
    ], { cwd: projectDir })
    expect(existsSync(installedSkillMd)).toBe(false)

    // Remote Worker skill sync (`--worker`) targeting a standard provider target (`claude`).
    await runCLI(cli, [
      'skill',
      'install',
      'leapmux-delegate',
      '--worker',
      leapmuxServer.workerId,
      '--target',
      'claude',
    ])

    const remoteStatus = await runCLI(cli, [
      'skill',
      'status',
      '--worker',
      leapmuxServer.workerId,
    ])
    expect(remoteStatus).toBeTruthy()

    await runCLI(cli, [
      'skill',
      'uninstall',
      'leapmux-delegate',
      '--worker',
      leapmuxServer.workerId,
      '--force',
    ])
  })
})
