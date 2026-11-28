/**
 * End-to-end coverage for file, git, and worktree CLI commands against a live
 * Hub, Worker, real Git repositories, and browser UI:
 *
 *   file roots|list|stat|read|search|glob|open|set,
 *   git status|read|checkout|branch list|branch create|branch delete|branch inspect|push|diff|log|commit,
 *   worktree list|get|create|remove|inspect.
 */

import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'
import { tabById } from './helpers/ui'
import { commitFile, initGitRepo } from './helpers/worktree'

test.describe('control CLI files, git, and worktrees', () => {
  test('browses, reads, searches, globs, opens, and configures file tabs on a worker', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const dir = createTestDirectory('leapmux-cli-files-')
    initGitRepo(dir)
    commitFile(dir, 'hello.txt', 'alpha line\nneedle token 42\nomega line\n', 'add hello.txt')
    commitFile(dir, 'src/index.ts', 'export const answer = 42\n', 'add src/index.ts')
    writeFileSync(join(dir, 'hello.txt'), 'alpha line\nneedle token 99\nomega line\n')

    const wFlags = ['--worker', leapmuxServer.workerId]

    const roots = await runCLI(cli, [
      'file',
      'roots',
      ...wFlags,
    ]) as { roots: string[] }
    expect(roots.roots.length).toBeGreaterThan(0)

    const listed = await runCLI(cli, [
      'file',
      'list',
      dir,
      '--max-depth',
      '2',
      ...wFlags,
    ]) as { listings: Array<{ entries: Array<{ name: string, path: string }> }> }
    const allEntries = listed.listings.flatMap(l => l.entries)
    expect(allEntries.some(e => e.name === 'hello.txt')).toBe(true)

    const st = await runCLI(cli, [
      'file',
      'stat',
      join(dir, 'hello.txt'),
      ...wFlags,
    ]) as { exists?: boolean, size?: number, is_dir?: boolean }
    expect(st.size).toBeGreaterThan(0)

    const read = await runCLI(cli, [
      'file',
      'read',
      join(dir, 'hello.txt'),
      ...wFlags,
    ]) as { content: string }
    expect(read.content).toContain('needle token 99')

    const searched = await runCLI(cli, [
      'file',
      'search',
      'needle token',
      dir,
      ...wFlags,
    ]) as { matches: Array<{ path: string, line?: number }> }
    expect(searched.matches.length).toBeGreaterThan(0)

    const globbed = await runCLI(cli, [
      'file',
      'glob',
      '**/*.ts',
      dir,
      ...wFlags,
    ]) as { paths?: string[], files?: string[], matches?: string[] }
    const hits = globbed.paths ?? globbed.files ?? globbed.matches ?? []
    expect(hits.some(p => p.endsWith('index.ts'))).toBe(true)

    // Open the modified file in the live workspace and change its view mode via `file set`.
    const opened = await runCLI(cli, [
      'file',
      'open',
      join(dir, 'hello.txt'),
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--dir',
      dir,
      ...wFlags,
    ]) as { id: string }
    expect(opened.id).toBeTruthy()
    await expect(tabById(page, opened.id)).toBeVisible()

    await runCLI(cli, [
      'file',
      'set',
      opened.id,
      '--view-mode',
      'unified_diff',
      '--diff-base',
      'head-vs-working',
    ])

    await runCLI(cli, [
      'tab',
      'close',
      opened.id,
      '--worktree',
      'keep',
    ])
    await expect(tabById(page, opened.id)).toHaveCount(0)
  })

  test('exercises all git and worktree commands against a real repository and bare remote', async ({ authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const bareRemote = createTestDirectory('leapmux-cli-bare-remote-')
    execFileSync('git', ['init', '--bare', bareRemote], { encoding: 'utf8' })

    const repoDir = createTestDirectory('leapmux-cli-git-repo-')
    initGitRepo(repoDir)
    commitFile(repoDir, 'README.md', '# CLI Git E2E\n', 'initial commit')
    execFileSync('git', ['remote', 'add', 'origin', bareRemote], { cwd: repoDir, encoding: 'utf8' })
    execFileSync('git', ['push', '-u', 'origin', 'main'], { cwd: repoDir, encoding: 'utf8' })

    // Open a tab in `repoDir` so Worker git-push authorization recognizes the directory.
    const repoTab = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--dir',
      repoDir,
    ]) as { id: string }

    const gitFlags = [
      '--dir',
      repoDir,
      '--worker',
      leapmuxServer.workerId,
    ]

    const status = await runCLI(cli, [
      'git',
      'status',
      ...gitFlags,
    ]) as { branch: string, is_git_repo?: boolean }
    expect(status.branch).toBe('main')

    const readHead = await runCLI(cli, [
      'git',
      'read',
      join(repoDir, 'README.md'),
      '--ref',
      'HEAD',
      '--worker',
      leapmuxServer.workerId,
    ]) as { content: string }
    expect(readHead.content).toContain('# CLI Git E2E')

    // Branch lifecycle: create, list, inspect, checkout, commit, diff, log, push, delete.
    await runCLI(cli, [
      'git',
      'branch',
      'create',
      'feat/cli-e2e',
      '--base',
      'main',
      ...gitFlags,
    ])
    const branches = await runCLI(cli, [
      'git',
      'branch',
      'list',
      ...gitFlags,
    ]) as { branches: Array<{ name: string }> }
    expect(branches.branches.some(b => b.name === 'feat/cli-e2e')).toBe(true)

    const inspected = await runCLI(cli, [
      'git',
      'branch',
      'inspect',
      'feat/cli-e2e',
      ...gitFlags,
    ]) as { branch_name: string }
    expect(inspected.branch_name).toBe('feat/cli-e2e')

    await runCLI(cli, [
      'git',
      'checkout',
      'feat/cli-e2e',
      ...gitFlags,
    ])

    writeFileSync(join(repoDir, 'README.md'), '# CLI Git E2E\nUpdated via E2E.\n')
    const diff = await runCLI(cli, [
      'git',
      'diff',
      ...gitFlags,
    ]) as { diff?: string, patch?: string, files?: unknown[] }
    expect(JSON.stringify(diff)).toContain('Updated via E2E.')

    await runCLI(cli, [
      'git',
      'commit',
      '--all',
      '-m',
      'feat: update README from CLI E2E',
      ...gitFlags,
    ])

    const log = await runCLI(cli, [
      'git',
      'log',
      '--limit',
      '5',
      ...gitFlags,
    ]) as { commits: Array<{ subject?: string, message?: string }> }
    expect(log.commits.some(c => `${c.subject ?? ''} ${c.message ?? ''}`.includes('update README from CLI E2E'))).toBe(true)

    await runCLI(cli, [
      'git',
      'push',
      ...gitFlags,
    ])
    const remoteHeads = execFileSync('git', ['branch'], { cwd: bareRemote, encoding: 'utf8' })
    expect(remoteHeads).toContain('feat/cli-e2e')

    // Delete current branch with default fallback to main (testing optional --switch-to).
    await runCLI(cli, [
      'git',
      'branch',
      'delete',
      'feat/cli-e2e',
      ...gitFlags,
    ])
    const afterDelete = await runCLI(cli, [
      'git',
      'status',
      ...gitFlags,
    ]) as { branch: string }
    expect(afterDelete.branch).toBe('main')

    // --- Worktrees: create, list, get, inspect, remove ---
    const wtCreated = await runCLI(cli, [
      'worktree',
      'create',
      'feat/cli-wt',
      '--base',
      'main',
      ...gitFlags,
    ]) as { path: string, branch: string }
    expect(wtCreated.path).toBeTruthy()
    expect(existsSync(wtCreated.path)).toBe(true)

    // Creating a worktree must automatically exclude `.leapmux/tmp/` in `.git/info/exclude`.
    const excludeFile = join(repoDir, '.git', 'info', 'exclude')
    expect(readFileSync(excludeFile, 'utf8')).toContain('.leapmux/tmp/')

    const wtList = await runCLI(cli, [
      'worktree',
      'list',
      ...gitFlags,
    ]) as { worktrees: Array<{ path: string, branch?: string }> }
    expect(wtList.worktrees.some(w => w.branch === 'feat/cli-wt')).toBe(true)

    const wtGot = await runCLI(cli, [
      'worktree',
      'get',
      'branch:feat/cli-wt',
      ...gitFlags,
    ]) as { path: string, branch: string }
    expect(wtGot.branch).toBe('feat/cli-wt')

    const wtInspect = await runCLI(cli, [
      'worktree',
      'inspect',
      wtCreated.path,
      ...gitFlags,
    ]) as { path?: string, branch?: string }
    expect(wtInspect).toBeTruthy()

    await runCLI(cli, [
      'worktree',
      'remove',
      wtCreated.path,
      '--delete-branch',
      ...gitFlags,
    ])
    expect(existsSync(wtCreated.path)).toBe(false)

    await runCLI(cli, [
      'terminal',
      'close',
      repoTab.id,
      '--allow-busy',
      '--worktree',
      'keep',
    ])
  })
})
