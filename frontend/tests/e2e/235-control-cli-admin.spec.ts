/**
 * End-to-end coverage for all 54 `admin` CLI leaf commands against an isolated
 * Hub and Worker harness (`startMultiWorkerHarness`) using a real interactive
 * elevated OAuth credential (`mintInteractiveOAuthCLI`):
 *
 *   admin settings list|get|set|set-secret|reset,
 *   admin user list|get|create|update|grant-admin|revoke-admin|reset-password|delete,
 *   admin session list|revoke|revoke-user|purge-expired,
 *   admin worker list|get|deregister|reg-key list|reg-key revoke|reg-key purge-expired,
 *   admin app list|register|update|verify|unverify|allow-elevation|deny-elevation|revoke|delete,
 *   admin idp add|list|disable|enable|rotate-secret|remove,
 *   admin captcha get|set|disable|enable|reset,
 *   admin rate-limit list|set|disable|enable|reset,
 *   admin api-token list|issue|revoke,
 *   admin delegation-token list|revoke,
 *   admin network status.
 */

import { expect } from '@playwright/test'
import { test } from './fixtures'
import { loginViaAPI } from './helpers/api'
import { CLIError, mintInteractiveOAuthCLI, runCLI } from './helpers/cli'
import { startMultiWorkerHarness } from './helpers/multiWorker'
import { getGlobalState } from './helpers/server'

test.describe('control CLI admin commands', () => {
  test('manages settings, users, sessions, tokens, apps, idps, captcha, rate limits, workers, and network status', async () => {
    const harness = await startMultiWorkerHarness(1)
    try {
      const cli = await mintInteractiveOAuthCLI(harness)
      const primaryWorkerId = harness.workers[0]!.id

      // Negative guard: admin commands must refuse to run inside a LeapMux tab IPC shell.
      await expect(runCLI(cli, ['admin', 'network', 'status'], {
        env: { LEAPMUX_CONTROL_SOCK: '/tmp/leapmux-forbidden-tab.sock' },
      })).rejects.toBeInstanceOf(CLIError)

      // 1. `admin network status`
      const netStatus = await runCLI(cli, ['admin', 'network', 'status']) as {
        bound?: Array<{ address: string }>
        interfaces?: Array<{ name: string }>
      }
      expect((netStatus.bound ?? []).length).toBeGreaterThan(0)

      // 2. `admin settings` (5 commands: list, get, set, set-secret, reset)
      const allSettings = await runCLI(cli, ['admin', 'settings', 'list']) as {
        settings: Array<{ key: string }>
      }
      expect(allSettings.settings.some(s => s.key === 'signup_enabled')).toBe(true)

      const gotSetting = await runCLI(cli, [
        'admin',
        'settings',
        'get',
        'signup_enabled',
      ]) as {
        key: string
        customized: boolean
      }
      expect(gotSetting.key).toBe('signup_enabled')

      const setPublic = await runCLI(cli, [
        'admin',
        'settings',
        'set',
        'signup_enabled',
        'false',
      ]) as { key: string, customized: boolean, effective_json: boolean }
      expect(setPublic.customized).toBe(true)
      expect(setPublic.effective_json).toBe(false)

      const setSecret = await runCLI(cli, [
        'admin',
        'settings',
        'set-secret',
        'smtp',
        '{"password":"e2e-smtp-secret"}',
      ]) as { key: string, secret_set?: Record<string, boolean> }
      expect(setSecret.key).toBe('smtp')
      expect(setSecret.secret_set?.password).toBe(true)

      const resetPublic = await runCLI(cli, [
        'admin',
        'settings',
        'reset',
        'signup_enabled',
      ]) as {
        customized: boolean
      }
      expect(resetPublic.customized).toBe(false)
      await runCLI(cli, ['admin', 'settings', 'reset', 'smtp'])

      // 3. `admin user` (8 commands: create, list, get, update, grant-admin, revoke-admin, reset-password, delete)
      const aliceUser = `alice-e2e-${Date.now()}`
      const alicePass1 = 'Alice-Pass-One-2026!'
      const alicePass2 = 'Alice-Pass-Two-2026!'
      const createdUser = await runCLI(cli, [
        'admin',
        'user',
        'create',
        aliceUser,
        '--password',
        alicePass1,
        '--display-name',
        'Alice Initial',
        '--email',
        'alice@e2e.local',
        '--email-verified',
      ]) as { id: string, username: string, is_admin: boolean, email_verified: boolean }
      expect(createdUser.username).toBe(aliceUser)
      expect(createdUser.is_admin).toBe(false)
      expect(createdUser.email_verified).toBe(true)

      const userList = await runCLI(cli, [
        'admin',
        'user',
        'list',
        '--query',
        aliceUser,
      ]) as { users: Array<{ id: string, username: string }> }
      expect(userList.users.some(u => u.id === createdUser.id)).toBe(true)

      const userGet = await runCLI(cli, ['admin', 'user', 'get', aliceUser]) as {
        id: string
        display_name: string
      }
      expect(userGet.id).toBe(createdUser.id)
      expect(userGet.display_name).toBe('Alice Initial')

      const userUpdated = await runCLI(cli, [
        'admin',
        'user',
        'update',
        aliceUser,
        '--display-name',
        'Alice Updated',
      ]) as { display_name: string }
      expect(userUpdated.display_name).toBe('Alice Updated')

      const granted = await runCLI(cli, [
        'admin',
        'user',
        'grant-admin',
        aliceUser,
      ]) as {
        is_admin: boolean
      }
      expect(granted.is_admin).toBe(true)

      const revokedAdmin = await runCLI(cli, [
        'admin',
        'user',
        'revoke-admin',
        aliceUser,
      ]) as {
        is_admin: boolean
      }
      expect(revokedAdmin.is_admin).toBe(false)

      // 4. `admin session` (4 commands: list, revoke, revoke-user, purge-expired)
      await loginViaAPI(harness.hubUrl, aliceUser, alicePass1)
      await loginViaAPI(harness.hubUrl, aliceUser, alicePass1)

      const aliceSessions = await runCLI(cli, [
        'admin',
        'session',
        'list',
        '--user',
        aliceUser,
      ]) as { sessions: Array<{ id: string, username: string }> }
      expect(aliceSessions.sessions.length).toBeGreaterThanOrEqual(2)

      const firstSessionId = aliceSessions.sessions[0]!.id
      const revokedOneSession = await runCLI(cli, [
        'admin',
        'session',
        'revoke',
        firstSessionId,
      ]) as {
        revoked: string
      }
      expect(revokedOneSession.revoked).toBe(firstSessionId)

      await runCLI(cli, [
        'admin',
        'session',
        'revoke-user',
        aliceUser,
      ])
      const afterRevokeAll = await runCLI(cli, [
        'admin',
        'session',
        'list',
        '--user',
        aliceUser,
      ]) as { sessions: Array<{ id: string }> }
      expect(afterRevokeAll.sessions).toHaveLength(0)

      const purgedSessions = await runCLI(cli, ['admin', 'session', 'purge-expired']) as {
        purged: number
      }
      expect(purgedSessions.purged).toBeGreaterThanOrEqual(0)

      // 5. `admin api-token` (3 commands: issue, list, revoke)
      const issuedToken = await runCLI(cli, [
        'admin',
        'api-token',
        'issue',
        aliceUser,
        '--installation-name',
        'e2e-ci-runner',
        '--scope',
        'workspace:read',
        '--ttl',
        '600',
      ]) as { token_id: string, access_token: string }
      expect(issuedToken.token_id).toBeTruthy()
      expect(issuedToken.access_token).toBeTruthy()

      const listedTokens = await runCLI(cli, [
        'admin',
        'api-token',
        'list',
        '--user',
        aliceUser,
      ]) as { tokens: Array<{ id: string }> }
      expect(listedTokens.tokens.some(t => t.id === issuedToken.token_id)).toBe(true)

      const revokedToken = await runCLI(cli, [
        'admin',
        'api-token',
        'revoke',
        issuedToken.token_id,
      ]) as { revoked: string }
      expect(revokedToken.revoked).toBe(issuedToken.token_id)

      // Finish user lifecycle: `reset-password` + confirm new password signs in + `delete`.
      const resetPw = await runCLI(cli, [
        'admin',
        'user',
        'reset-password',
        aliceUser,
        '--password',
        alicePass2,
      ]) as { user_id: string, username: string }
      expect(resetPw.user_id).toBe(createdUser.id)
      const signedInAfterReset = await loginViaAPI(harness.hubUrl, aliceUser, alicePass2)
      expect(signedInAfterReset).toBeTruthy()

      const deletedUser = await runCLI(cli, ['admin', 'user', 'delete', aliceUser]) as {
        deleted: boolean
      }
      expect(deletedUser.deleted).toBe(true)

      // 6. `admin worker` (6 commands: list, get, deregister, reg-key list|revoke|purge-expired)
      const adminWorkers = await runCLI(cli, ['admin', 'worker', 'list']) as {
        workers: Array<{ id: string }>
      }
      expect(adminWorkers.workers.some(w => w.id === primaryWorkerId)).toBe(true)

      const adminWorkerGot = await runCLI(cli, [
        'admin',
        'worker',
        'get',
        primaryWorkerId,
      ]) as {
        id: string
      }
      expect(adminWorkerGot.id).toBe(primaryWorkerId)

      const extraWorker = await harness.addWorker('admin-dereg')
      const dereg = await runCLI(cli, [
        'admin',
        'worker',
        'deregister',
        extraWorker.id,
      ]) as {
        deregistered: string
      }
      expect(dereg.deregistered).toBe(extraWorker.id)

      const createdRegKey = await runCLI(cli, ['worker', 'key', 'create']) as { key: string }
      expect(createdRegKey.key).toBeTruthy()

      const regKeys = await runCLI(cli, ['admin', 'worker', 'reg-key', 'list']) as {
        keys: Array<{ id: string }>
      }
      expect(regKeys.keys.length).toBeGreaterThan(0)
      const regKeyId = regKeys.keys[0]!.id

      const revokedRegKey = await runCLI(cli, [
        'admin',
        'worker',
        'reg-key',
        'revoke',
        regKeyId,
      ]) as { revoked: string }
      expect(revokedRegKey.revoked).toBe(regKeyId)

      const purgedRegKeys = await runCLI(cli, [
        'admin',
        'worker',
        'reg-key',
        'purge-expired',
      ]) as {
        purged: number
      }
      expect(purgedRegKeys.purged).toBeGreaterThanOrEqual(0)

      // 7. `admin app` (9 commands: register, list, update, verify, unverify, allow-elevation, deny-elevation, delete, revoke)
      const app1 = await runCLI(cli, [
        'admin',
        'app',
        'register',
        'E2E Release Bot',
        '--scope',
        'workspace:read',
        '--redirect-uri',
        'http://127.0.0.1/callback',
        '--type',
        'confidential',
      ]) as { client_id: string, client_secret?: string, client_name: string }
      expect(app1.client_id).toBeTruthy()
      expect(app1.client_secret).toBeTruthy()

      const apps = await runCLI(cli, ['admin', 'app', 'list']) as {
        apps: Array<{ client_id: string }>
      }
      expect(apps.apps.some(a => a.client_id === app1.client_id)).toBe(true)

      const appUpdated = await runCLI(cli, [
        'admin',
        'app',
        'update',
        app1.client_id,
        '--name',
        'E2E Release Bot v2',
      ]) as { client_name: string }
      expect(appUpdated.client_name).toBe('E2E Release Bot v2')

      const appVerified = await runCLI(cli, [
        'admin',
        'app',
        'verify',
        app1.client_id,
      ]) as {
        verified: boolean
      }
      expect(appVerified.verified).toBe(true)

      const appUnverified = await runCLI(cli, [
        'admin',
        'app',
        'unverify',
        app1.client_id,
      ]) as {
        verified: boolean
      }
      expect(appUnverified.verified).toBe(false)

      const appElevated = await runCLI(cli, [
        'admin',
        'app',
        'allow-elevation',
        app1.client_id,
      ]) as {
        elevation_allowed: boolean
      }
      expect(appElevated.elevation_allowed).toBe(true)

      const appDenied = await runCLI(cli, [
        'admin',
        'app',
        'deny-elevation',
        app1.client_id,
      ]) as {
        elevation_allowed: boolean
      }
      expect(appDenied.elevation_allowed).toBe(false)

      const appDeleted = await runCLI(cli, [
        'admin',
        'app',
        'delete',
        app1.client_id,
      ]) as {
        deleted: string
      }
      expect(appDeleted.deleted).toBe(app1.client_id)

      const app2 = await runCLI(cli, [
        'admin',
        'app',
        'register',
        'E2E Revoke Target',
        '--scope',
        'workspace:read',
        '--redirect-uri',
        'http://127.0.0.1/cb2',
      ]) as { client_id: string }
      const appRevoked = await runCLI(cli, [
        'admin',
        'app',
        'revoke',
        app2.client_id,
      ]) as {
        revoked: string
      }
      expect(appRevoked.revoked).toBe(app2.client_id)

      // 8. `admin idp` (6 commands: add, list, disable, enable, rotate-secret, remove)
      const idp = await runCLI(cli, [
        'admin',
        'idp',
        'add',
        '--type',
        'github',
        '--name',
        'E2E GitHub',
        '--client-id',
        'Iv1.e2eclientid',
        '--client-secret',
        'e2e-initial-secret',
      ]) as { id: string, enabled: boolean }
      expect(idp.id).toBeTruthy()
      expect(idp.enabled).toBe(true)

      const idps = await runCLI(cli, ['admin', 'idp', 'list']) as {
        providers: Array<{ id: string }>
      }
      expect(idps.providers.some(p => p.id === idp.id)).toBe(true)

      const idpDisabled = await runCLI(cli, ['admin', 'idp', 'disable', idp.id]) as {
        enabled: boolean
      }
      expect(idpDisabled.enabled).toBe(false)

      const idpEnabled = await runCLI(cli, ['admin', 'idp', 'enable', idp.id]) as {
        enabled: boolean
      }
      expect(idpEnabled.enabled).toBe(true)

      const idpRotated = await runCLI(cli, [
        'admin',
        'idp',
        'rotate-secret',
        idp.id,
        '--client-secret',
        'e2e-rotated-secret',
      ]) as { id: string }
      expect(idpRotated.id).toBe(idp.id)

      const idpRemoved = await runCLI(cli, ['admin', 'idp', 'remove', idp.id]) as {
        removed: string
      }
      expect(idpRemoved.removed).toBe(idp.id)

      // 9. `admin captcha` (5 commands: get, set, disable, enable, reset)
      const captchaBefore = await runCLI(cli, ['admin', 'captcha', 'get']) as Record<string, unknown>
      expect(Object.keys(captchaBefore).length).toBeGreaterThan(0)

      const captchaSet = await runCLI(cli, [
        'admin',
        'captcha',
        'set',
        '--provider',
        'turnstile',
        '--site-key',
        '0x4AAA-E2E',
        '--secret',
        'e2e-turnstile-secret',
      ]) as { updated: boolean, provider: string }
      expect(captchaSet.updated).toBe(true)
      expect(captchaSet.provider).toBe('turnstile')

      const captchaDisabled = await runCLI(cli, ['admin', 'captcha', 'disable']) as Record<string, boolean>
      expect(captchaDisabled['captcha.enabled']).toBe(false)

      const captchaEnabled = await runCLI(cli, ['admin', 'captcha', 'enable']) as Record<string, boolean>
      expect(captchaEnabled['captcha.enabled']).toBe(true)

      const captchaReset = await runCLI(cli, ['admin', 'captcha', 'reset']) as {
        reset: string[]
      }
      expect(captchaReset.reset.length).toBeGreaterThan(0)

      // 10. `admin rate-limit` (5 commands: list, set, disable, enable, reset)
      const rateLimits = await runCLI(cli, ['admin', 'rate-limit', 'list']) as {
        rate_limits: Array<{ key: string }>
      }
      expect(rateLimits.rate_limits.length).toBeGreaterThan(0)

      const rlSet = await runCLI(cli, [
        'admin',
        'rate-limit',
        'set',
        'elevation',
        '--max-attempts',
        '7',
        '--window',
        '600',
      ]) as { customized: boolean }
      expect(rlSet.customized).toBe(true)

      const rlDisabled = await runCLI(cli, [
        'admin',
        'rate-limit',
        'disable',
        'elevation',
      ]) as {
        effective_json: { enabled?: boolean }
      }
      expect(rlDisabled.effective_json.enabled).toBe(false)

      const rlEnabled = await runCLI(cli, [
        'admin',
        'rate-limit',
        'enable',
        'elevation',
      ]) as {
        effective_json: { enabled?: boolean }
      }
      expect(rlEnabled.effective_json.enabled).toBe(true)

      const rlReset = await runCLI(cli, [
        'admin',
        'rate-limit',
        'reset',
        'elevation',
      ]) as {
        customized: boolean
      }
      expect(rlReset.customized).toBe(false)

      // 11. `admin delegation-token` (2 commands: list, revoke)
      // Trigger a real worker-minted delegation token by invoking `leapmux whoami`
      // from inside a live terminal tab on the worker (using its LEAPMUX_CONTROL_SOCKET).
      const ws = await runCLI(cli, ['workspace', 'create', 'Delegation Token E2E']) as { id: string }
      const term = await runCLI(cli, [
        'terminal',
        'open',
        '--workspace',
        ws.id,
        '--worker',
        primaryWorkerId,
      ]) as { id: string }
      const { binaryPath } = getGlobalState()
      await runCLI(cli, [
        'terminal',
        'run',
        term.id,
        `${binaryPath} whoami --json`,
      ])

      let delTokens: Array<{ id: string, worker_id: string }> = []
      await expect.poll(async () => {
        const listed = await runCLI(cli, ['admin', 'delegation-token', 'list']) as {
          tokens: Array<{ id: string, worker_id: string }>
        }
        delTokens = listed.tokens
        return delTokens.length
      }).toBeGreaterThan(0)

      const delTokenId = delTokens[0]!.id
      const revokedDel = await runCLI(cli, [
        'admin',
        'delegation-token',
        'revoke',
        delTokenId,
      ]) as { revoked: string }
      expect(revokedDel.revoked).toBe(delTokenId)
    }
    finally {
      await harness.stop()
    }
  })
})
