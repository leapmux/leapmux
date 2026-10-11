import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { createWorkspaceViaAPI, openAgentViaAPI } from './api'

/** Run one git command in `cwd` with no shell, so a path or a name reaches git as one argument. */
function git(cwd: string, args: string[]): string {
  return execFileSync('git', args, { cwd, encoding: 'utf8' })
}

/**
 * The settings that every test repository pins, so that the machine's global gitconfig cannot change them.
 *
 * Each of the first three would otherwise put a SECOND writer inside `.git` that the test never asked for:
 *
 * - `core.fsmonitor` (commonly on for macOS dev machines) makes git spawn a
 *   filesystem-monitor daemon per repo. It outlives the command that started
 *   it, touches the index, and leaves a unix socket behind -- so it competes
 *   for `index.lock` with the worker's own git commands and survives into the
 *   test's cleanup.
 * - `gc.auto` / `maintenance.auto` are the same hazard in slower motion: a
 *   commit can fire a background `git gc` that writes after the command
 *   returns.
 *
 * The identity lets a test commit on a host with no global `user.name`.
 * A linked worktree reads the config of its main repository, so it needs no copy of these settings.
 */
const PINNED_GIT_CONFIG: ReadonlyArray<readonly [string, string]> = [
  ['core.fsmonitor', 'false'],
  ['gc.auto', '0'],
  ['maintenance.auto', 'false'],
  ['user.email', 'test@test.com'],
  ['user.name', 'Test'],
]

/** Write the pinned settings into the config of the repository at `repoDir`. */
function pinGitConfig(repoDir: string): void {
  for (const [key, value] of PINNED_GIT_CONFIG)
    git(repoDir, ['config', key, value])
}

/**
 * Initialize an empty repository at `dir` on branch `main`, with the pinned settings.
 *
 * `--initial-branch=main` is pinned for the same reason it is in the Go
 * helpers: a host without `init.defaultBranch` lands on `master`, and the
 * specs address the initial branch by name. The init itself runs with
 * `core.fsmonitor=false`, because no repository config exists yet to pin it.
 */
export function initGitRepo(dir: string): void {
  mkdirSync(dir, { recursive: true })
  git(dir, ['-c', 'core.fsmonitor=false', 'init', '--initial-branch=main'])
  pinGitConfig(dir)
}

/**
 * Write `content` to `path` inside the repository, stage that file, and commit it with `message`.
 * The parent directories of `path` are created first.
 */
export function commitFile(repoDir: string, path: string, content: string, message: string): void {
  const file = join(repoDir, path)
  mkdirSync(dirname(file), { recursive: true })
  writeFileSync(file, content)
  git(repoDir, ['add', '--', path])
  git(repoDir, ['commit', '-m', message])
}

/**
 * Create a git repo inside the server's data directory so the worker can access it.
 * The repository holds one commit of `README.md` on `main`, and carries the pinned settings of `initGitRepo`.
 */
export function createGitRepo(dataDir: string, name: string): string {
  const repoDir = join(dataDir, name)
  initGitRepo(repoDir)
  commitFile(repoDir, 'README.md', '# Test\n', 'init')
  return repoDir
}

/**
 * Make `dir` the root of a git repository of its own, as `createGitRepo` makes one, unless it is the root of a work
 * tree already. A `.git` entry marks that root: a directory for a repository, and a file for a linked worktree.
 *
 * A project that a scenario prepares needs a root of its own when its provider reads the project configuration up to
 * the repository root. The rule of some providers makes each working directory such a root already
 * (`gitRepositoryWorkingDir` in `./providerWorkingDir.ts`), and a second `createGitRepo` there fails, because its
 * README commit has nothing to commit.
 *
 * The function returns nothing, so it cannot give the brand of a `ProviderWorkingDir` (`./providerWorkingDir.ts`) to a
 * directory that a test made by hand. A project keeps the brand that the rule of its provider gave it.
 */
export function ensureGitRepositoryRoot(dir: string): void {
  if (!existsSync(join(dir, '.git')))
    createGitRepo(dir, '.')
}

/**
 * Create a bare repository as a remote, and a clone of it whose `main` holds one pushed commit, so the clone's
 * branch has an upstream. The bare repository is `<dataDir>/<name>-bare`, and the clone is `<dataDir>/<name>`.
 * Both carry the pinned settings.
 */
export function createGitRepoWithRemote(dataDir: string, name: string): { repoDir: string, bareDir: string } {
  const bareDir = join(dataDir, `${name}-bare`)
  mkdirSync(bareDir, { recursive: true })
  git(bareDir, ['-c', 'core.fsmonitor=false', 'init', '--bare', '--initial-branch=main'])
  pinGitConfig(bareDir)
  const repoDir = join(dataDir, name)
  git(dataDir, ['-c', 'core.fsmonitor=false', 'clone', bareDir, repoDir])
  // The clone of an empty repository takes its unborn branch from the host's `init.defaultBranch`, not from the
  // remote, so the branch is named here.
  git(repoDir, ['symbolic-ref', 'HEAD', 'refs/heads/main'])
  pinGitConfig(repoDir)
  commitFile(repoDir, 'README.md', '# Test\n', 'init')
  git(repoDir, ['push', '-u', 'origin', 'HEAD'])
  return { repoDir, bareDir }
}

/**
 * Add a linked worktree of `repoDir` at `<dataDir>/<name>` on the new branch `branch`, and return its real path.
 * The real path, because the Worker reports a resolved path: the temporary directory of macOS is a symlink
 * (`/var` to `/private/var`).
 */
export function addWorktree(repoDir: string, dataDir: string, name: string, branch: string): string {
  const target = join(dataDir, name)
  git(repoDir, ['worktree', 'add', target, '-b', branch])
  return realpathSync(target)
}

/**
 * Check if a git branch exists in a repository.
 * The check reads the exact ref `refs/heads/<branchName>`. A `git branch --list` lookup treats the name as a pattern,
 * and git reads a name that starts with `-` as an option.
 */
export function branchExists(repoDir: string, branchName: string): boolean {
  try {
    git(repoDir, ['show-ref', '--verify', '--quiet', `refs/heads/${branchName}`])
    return true
  }
  catch (error) {
    // show-ref exits with 1 for an absent ref. Another status is a real failure, such as a directory that is not a repository.
    if ((error as { status?: unknown }).status === 1)
      return false
    throw error
  }
}

/**
 * How long a filesystem effect of a worker-side git operation may take.
 *
 * Deliberately far below the suite's 120s assertion budget, and measured rather
 * than guessed: every SUCCESSFUL worktree create/remove in these specs lands
 * within a second or two even with eight workers competing for git, while the
 * failures observed under load never complete at all -- the add dies on a stale
 * index.lock and nothing ever appears. So a long budget buys no passes and
 * costs minutes of wall time per failing test. 30s is roughly an order of
 * magnitude of headroom over the slowest success seen.
 */
const GIT_FS_EFFECT_TIMEOUT_MS = 30_000

/**
 * Poll until a path exists on disk (worktree creation is async).
 *
 * The RPC that creates a worktree returns once the worker has accepted the
 * request; the `git worktree add` runs on the startup goroutine afterwards, so
 * a one-shot `existsSync` right after the dialog closes is a coin flip.
 */
export async function waitForPathExists(path: string): Promise<void> {
  await expect(() => {
    expect(existsSync(path), `path should exist: ${path}`).toBe(true)
  }).toPass({ timeout: GIT_FS_EFFECT_TIMEOUT_MS })
}

/**
 * Poll until `repoDir` has `branch` checked out.
 *
 * Every git-mode checkout runs on the worker's async startup goroutine, which
 * starts only after OpenAgent has answered -- so by the time the RPC returns or
 * the create-workspace dialog closes, HEAD has usually not moved yet. A one-shot
 * `git rev-parse` there is a race that reads `main` and reports the feature as
 * broken. Shared so the UI and API variants of the same assertion cannot drift:
 * the API one already polled, the UI one did not, and only the UI one flaked.
 */
export async function expectRepoBranch(repoDir: string, branch: string): Promise<void> {
  await expect
    .poll(() => git(repoDir, ['rev-parse', '--abbrev-ref', 'HEAD']).trim())
    .toBe(branch)
}

/**
 * Poll until a path no longer exists on disk (worktree removal is async).
 * Same budget rationale as {@link waitForPathExists}.
 */
export async function waitForPathDeleted(path: string): Promise<void> {
  await expect(() => {
    expect(existsSync(path), `path should be gone: ${path}`).toBe(false)
  }).toPass({ timeout: GIT_FS_EFFECT_TIMEOUT_MS })
}

/**
 * The directory where the Worker places the managed worktree of `branch` for the repository of `repoDir`:
 * `<main repository>-worktrees/<branch>`, beside the main repository.
 *
 * The path is anchored on the MAIN REPO ROOT, matching the worker's placement convention. A path derived from
 * `repoDir` is wrong whenever `repoDir` is itself a linked worktree: the worker still places the new worktree beside
 * the main repository.
 */
export function managedWorktreePath(repoDir: string, branch: string): string {
  const repoRoot = mainRepoRoot(repoDir)
  return join(dirname(repoRoot), `${basename(repoRoot)}-worktrees`, branch)
}

/**
 * Create a workspace on the hub, then open an agent with worktree enabled.
 * Returns the workspace ID and the directory of the new worktree.
 */
export async function createWorkspaceWithWorktreeViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  title: string,
  workingDir: string,
  worktreeBranch: string,
): Promise<{ workspaceId: string, worktreeDir: string }> {
  const workspaceId = await createWorkspaceViaAPI(hubUrl, token, title)
  await openAgentViaAPI({ hubUrl, adminToken: token, workerId }, workspaceId, workingDir, {
    createWorktree: true,
    worktreeBranch,
  })

  // OpenAgent returns synchronously with status=STARTING and the worktree is
  // created asynchronously during phased startup (#194). Tests expect
  // `existsSync(worktreeDir)` to be true immediately after this helper returns,
  // so wait until the worker has actually materialized it on disk.
  const worktreeDir = managedWorktreePath(workingDir, worktreeBranch)
  await waitForPathExists(worktreeDir)
  return { workspaceId, worktreeDir }
}

/**
 * The MAIN repository's root for `dir`, even when `dir` is a linked worktree.
 *
 * `--show-toplevel` would answer the worktree's own root, which is exactly the
 * trap this exists to avoid. `--git-common-dir` resolves to the main repo's
 * `.git` from any worktree, so its parent is the main root.
 */
function mainRepoRoot(dir: string): string {
  const commonDir = git(dir, ['rev-parse', '--path-format=absolute', '--git-common-dir']).trim()
  return realpathSync(dirname(commonDir))
}
