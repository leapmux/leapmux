/**
 * End-to-end coverage for CLI metadata, diagnostics, identity, and persistent
 * context commands against a live Hub and Worker:
 *
 *   version, commands, completion, guide, whoami, doctor,
 *   context show, context use, context clear.
 */

import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI, runCLILocal, runCLILocalText, runCLIText } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'

test.describe('control CLI meta, identity, and context', () => {
  test('reports binary, hub, and worker versions and lists command metadata', async ({ leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    const ver = await runCLI(cli, ['version']) as {
      cli?: { version?: string }
      version?: string
      hub?: { version?: string }
    }
    expect(ver.cli?.version || ver.version).toBeTruthy()

    const allCmds = await runCLILocal(['commands']) as {
      commands: Array<{ path: string, summary: string }>
    }
    expect(allCmds.commands.length).toBeGreaterThan(200)
    expect(allCmds.commands.some(c => c.path === 'agent fork')).toBe(true)

    const compactAgent = await runCLILocal([
      'commands',
      'agent',
      '--compact',
      '--depth',
      '1',
    ]) as { commands: Array<{ path: string }> }
    expect(compactAgent.commands.length).toBeGreaterThan(10)
    expect(compactAgent.commands.every(c => c.path.startsWith('agent'))).toBe(true)
  })

  test('prints shell completions and agent guides', async () => {
    for (const shell of [
      'bash',
      'zsh',
      'fish',
      'powershell',
      'elvish',
      'nushell',
    ]) {
      const res = await runCLILocalText(['completion', shell])
      expect(res.code).toBe(0)
      expect(res.stdout.length).toBeGreaterThan(50)
    }

    const topics = await runCLILocalText(['guide', '--topics'])
    expect(topics.stdout).toContain('safety')
    expect(topics.stdout).toContain('targets')
    expect(topics.stdout).toContain('exit-codes')

    const fullGuide = await runCLILocalText(['guide'])
    expect(fullGuide.stdout).toContain('leapmux')
  })

  test('identifies the caller with whoami and verifies health with doctor', async ({ leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    const who = await runCLI(cli, ['whoami']) as {
      username?: string
      user_id?: string
      hub_url?: string
    }
    expect(who.user_id).toBe(cli.userID)
    expect(who.username).toBeTruthy()

    const doc = await runCLI(cli, ['doctor']) as {
      ok?: boolean
      checks: Array<{ name: string, status: string }>
    }
    expect(Array.isArray(doc.checks)).toBe(true)
    expect(doc.checks.length).toBeGreaterThan(0)
    expect(doc.checks.some(c => c.status === 'ok')).toBe(true)
    expect(doc.checks.some(c => c.name === 'workers' && c.status === 'ok')).toBe(true)

    const docText = await runCLIText(cli, ['doctor'])
    expect(docText.stdout).toContain('workers')
  })

  test('sets, shows, applies, and clears persistent CLI context defaults', async ({ leapmuxServer, authenticatedWorkspace }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const isolatedHome = createTestDirectory('leapmux-cli-ctx-')
    const env = { HOME: isolatedHome, USERPROFILE: isolatedHome }

    const initial = await runCLILocal(['context', 'show'], { configDir: cli.path, env }) as {
      worker?: string
      workspace?: string
      provider?: string
    }
    expect(initial.worker ?? '').toBe('')

    const used = await runCLILocal([
      'context',
      'use',
      '--worker',
      leapmuxServer.workerId,
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--provider',
      'claude',
    ], { configDir: cli.path, env }) as {
      worker?: string
      workspace?: string
      provider?: string
    }
    expect(used).toMatchObject({
      worker: leapmuxServer.workerId,
      workspace: authenticatedWorkspace.workspaceId,
      provider: 'claude',
    })

    const shown = await runCLILocal(['context', 'show'], { configDir: cli.path, env }) as {
      worker?: string
      workspace?: string
      provider?: string
    }
    expect(shown).toMatchObject({
      worker: leapmuxServer.workerId,
      workspace: authenticatedWorkspace.workspaceId,
      provider: 'claude',
    })

    // Clear one field first, then clear all remaining fields.
    const partialClear = await runCLILocal([
      'context',
      'clear',
      '--field',
      'provider',
    ], { configDir: cli.path, env }) as {
      worker?: string
      provider?: string
    }
    expect(partialClear.worker).toBe(leapmuxServer.workerId)
    expect(partialClear.provider ?? '').toBe('')

    const cleared = await runCLILocal(['context', 'clear'], { configDir: cli.path, env }) as {
      worker?: string
      workspace?: string
      provider?: string
    }
    expect(cleared.worker ?? '').toBe('')
    expect(cleared.workspace ?? '').toBe('')
  })
})
