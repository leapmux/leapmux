import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { gitRepositoryWorkingDir } from './providerWorkingDir'
import { addWorktree, branchExists, commitFile, createGitRepo, createGitRepoWithRemote, ensureGitRepositoryRoot, initGitRepo, managedWorktreePath } from './worktree'

// The run directory of these tests is the scratch directory of the current test.
const runDirectory = vi.hoisted(() => ({ root: '' }))
vi.mock('./runDirectory', () => ({ createTestDirectory: (prefix: string) => mkdtempSync(join(runDirectory.root, prefix)) }))

/** Run git in `cwd` and return its trimmed output. */
function gitOutput(cwd: string, args: string[]): string {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim()
}

describe('git repository helpers', () => {
  let root: string

  beforeEach(() => {
    const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
    mkdirSync(scratch, { recursive: true })
    root = mkdtempSync(join(scratch, 'worktree-helpers-'))
    runDirectory.root = root
  })

  afterEach(() => {
    rmSync(root, { recursive: true, force: true })
  })

  it('initGitRepo pins every background writer off, sets an identity, and starts on main', () => {
    const dir = join(root, 'pinned')
    initGitRepo(dir)
    expect(gitOutput(dir, ['config', '--local', 'core.fsmonitor'])).toBe('false')
    expect(gitOutput(dir, ['config', '--local', 'gc.auto'])).toBe('0')
    expect(gitOutput(dir, ['config', '--local', 'maintenance.auto'])).toBe('false')
    expect(gitOutput(dir, ['config', '--local', 'user.email'])).toBe('test@test.com')
    expect(gitOutput(dir, ['config', '--local', 'user.name'])).toBe('Test')
    expect(gitOutput(dir, ['symbolic-ref', '--short', 'HEAD'])).toBe('main')
  })

  it('ensureGitRepositoryRoot makes a plain directory the root of a repository of its own, with the README commit', () => {
    const dir = join(root, 'project')
    mkdirSync(dir)
    writeFileSync(join(dir, 'AGENTS.md'), 'project instructions\n')
    ensureGitRepositoryRoot(dir)
    // The run directory sits inside the LeapMux checkout, so before the call git reported the checkout as its top.
    expect(gitOutput(dir, ['rev-parse', '--show-toplevel'])).toBe(realpathSync(dir))
    expect(gitOutput(dir, ['log', '--pretty=%s'])).toBe('init')
    expect(gitOutput(dir, ['config', '--local', 'core.fsmonitor'])).toBe('false')
    expect(gitOutput(dir, ['status', '--porcelain'])).toBe('?? AGENTS.md')
  })

  it('ensureGitRepositoryRoot leaves a directory that is the root of a repository already', () => {
    const dir = gitRepositoryWorkingDir('agent-wd-')
    writeFileSync(join(dir, 'AGENTS.md'), 'project instructions\n')
    ensureGitRepositoryRoot(dir)
    expect(gitOutput(dir, ['rev-parse', '--show-toplevel'])).toBe(realpathSync(dir))
    expect(gitOutput(dir, ['log', '--pretty=%s'])).toBe('init')
    expect(gitOutput(dir, ['status', '--porcelain'])).toBe('?? AGENTS.md')
  })

  it('ensureGitRepositoryRoot leaves the root of a linked worktree, whose .git is a file', () => {
    const repo = createGitRepo(root, 'repo')
    const linked = addWorktree(repo, root, 'linked', 'feature/linked')
    ensureGitRepositoryRoot(linked)
    expect(gitOutput(linked, ['rev-parse', '--show-toplevel'])).toBe(linked)
    expect(gitOutput(linked, ['rev-parse', '--abbrev-ref', 'HEAD'])).toBe('feature/linked')
    expect(gitOutput(linked, ['log', '--pretty=%s'])).toBe('init')
  })

  it('ensureGitRepositoryRoot makes a directory inside another repository the root of a repository of its own', () => {
    const repo = createGitRepo(root, 'outer')
    const nested = join(repo, 'nested')
    ensureGitRepositoryRoot(nested)
    expect(gitOutput(nested, ['rev-parse', '--show-toplevel'])).toBe(realpathSync(nested))
    expect(gitOutput(nested, ['log', '--pretty=%s'])).toBe('init')
  })

  it('createGitRepo commits the README on main', () => {
    const dir = createGitRepo(root, 'repo')
    expect(dir).toBe(join(root, 'repo'))
    expect(gitOutput(dir, ['rev-parse', '--abbrev-ref', 'HEAD'])).toBe('main')
    expect(gitOutput(dir, ['log', '--pretty=%s'])).toBe('init')
    expect(gitOutput(dir, ['status', '--porcelain'])).toBe('')
  })

  it('commitFile creates the parent directories and commits only that file', () => {
    const dir = createGitRepo(root, 'repo')
    commitFile(dir, 'pkg/deep/tracked.txt', 'hello\n', 'add pkg')
    expect(readFileSync(join(dir, 'pkg/deep/tracked.txt'), 'utf8')).toBe('hello\n')
    expect(gitOutput(dir, ['log', '-1', '--pretty=%s'])).toBe('add pkg')
    expect(gitOutput(dir, ['show', '--name-only', '--pretty=', 'HEAD'])).toBe('pkg/deep/tracked.txt')
  })

  it('addWorktree adds a linked worktree on a new branch and returns its real path', () => {
    const dir = createGitRepo(root, 'repo')
    const worktree = addWorktree(dir, root, 'nested/wt', 'wt-branch')
    expect(worktree).toBe(realpathSync(join(root, 'nested/wt')))
    expect(gitOutput(worktree, ['rev-parse', '--abbrev-ref', 'HEAD'])).toBe('wt-branch')
    expect(branchExists(dir, 'wt-branch')).toBe(true)
  })

  it('branchExists answers for an absent branch and passes a name that looks like a flag to git as a name', () => {
    const dir = createGitRepo(root, 'repo')
    expect(branchExists(dir, 'absent-branch')).toBe(false)
    expect(branchExists(dir, '--all')).toBe(false)
  })

  it('createGitRepoWithRemote gives the clone an upstream on main that the remote holds', () => {
    const { repoDir, bareDir } = createGitRepoWithRemote(root, 'cloned')
    expect(repoDir).toBe(join(root, 'cloned'))
    expect(bareDir).toBe(join(root, 'cloned-bare'))
    expect(gitOutput(repoDir, ['rev-parse', '--abbrev-ref', 'HEAD'])).toBe('main')
    expect(gitOutput(repoDir, ['rev-parse', '--abbrev-ref', '@{upstream}'])).toBe('origin/main')
    expect(gitOutput(bareDir, ['log', '-1', '--pretty=%s', 'main'])).toBe('init')
    expect(gitOutput(repoDir, ['config', '--local', 'core.fsmonitor'])).toBe('false')
  })

  it('managedWorktreePath places a worktree beside the MAIN repository, also from a linked worktree', () => {
    const dir = createGitRepo(root, 'repo')
    const expected = join(realpathSync(root), 'repo-worktrees', 'feature')
    expect(managedWorktreePath(dir, 'feature')).toBe(expected)
    const linked = addWorktree(dir, root, 'elsewhere/linked', 'linked-branch')
    expect(managedWorktreePath(linked, 'feature')).toBe(expected)
    expect(existsSync(expected)).toBe(false)
  })
})
