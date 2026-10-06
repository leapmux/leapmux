import type { ProviderAgent } from './workspace'
import { isAbsolute } from 'node:path'
import { createTestDirectory } from './runDirectory'
import { createGitRepo } from './worktree'

declare const providerWorkingDirBrand: unique symbol

/**
 * A directory where an agent of a stated provider can open.
 *
 * Only this module makes one, in two ways:
 *
 * - The working-directory rule of the provider makes it. A spec calls `newProviderWorkingDir` with the
 *   `ProviderAgent` of the provider, or `newNativeWorkingDir` (`./nativeAgentOpen.ts`) with a scenario context.
 *   `gitRepositoryWorkingDir` is the rule of each provider that reads configuration from the git repository around
 *   its directory.
 * - `deliberateWorkingDir` marks a layout that a test needs and that no rule makes. Its call states the reason.
 *
 * Each open of an agent of a stated provider requires this type: `openProviderAgent`, `openNativeAgent`, the
 * `openAgentViaAPI` call that states a provider, and the New Agent dialog of `./nativeResume.ts`. A plain string has
 * no brand, so a spec that opens such an agent in a directory that it made by hand fails to compile.
 *
 * A cast can still make the brand from a plain string. The `no-restricted-syntax` block for `tests/e2e` in
 * `eslint.config.ts` refuses that cast outside this module and outside a unit test, which opens no agent.
 */
export type ProviderWorkingDir = string & { readonly [providerWorkingDirBrand]: true }

/** Give the brand to a directory that a constructor of this module made or checked. */
function brand(directory: string): ProviderWorkingDir {
  return directory as ProviderWorkingDir
}

/**
 * Create a new working directory for an agent of `agent`, by the rule of its provider.
 * The name of the directory starts with `prefix`. The default prefix is `<agent prefix>-wd-`.
 *
 * A provider that states no rule gets a fresh private directory of the run.
 */
export function newProviderWorkingDir(agent: ProviderAgent, prefix = `${agent.prefix}-wd-`): ProviderWorkingDir {
  return agent.workingDir === undefined ? brand(createTestDirectory(prefix)) : agent.workingDir(prefix)
}

/**
 * Create a working directory that is the root of a git repository of its own, inside a new directory of the run whose
 * name starts with `prefix`.
 *
 * Some providers read configuration from the git repository around their working directory: rules, skills, steering
 * documents, hooks, an `AGENTS.md` or an `.mcp.json`. The run directory sits inside the LeapMux checkout, whose root
 * holds such files. A repository of its own holds none of them, so the agent reads none of the configuration of the
 * checkout. The `ProviderAgent` of each such provider states this rule (`./workspace.ts`).
 */
export function gitRepositoryWorkingDir(prefix: string): ProviderWorkingDir {
  return brand(createGitRepo(createTestDirectory(prefix), 'repo'))
}

/**
 * Mark `directory` as a working directory that a test lays out on purpose, where no rule of a provider applies.
 *
 * A rule makes a directory that holds nothing of the test. Some scenarios need more than that:
 *
 * - A path of the run outside the project of the agent. A provider that treats the git worktree of its directory as
 *   its project puts the whole LeapMux checkout, and so the whole run, inside the project of a plain directory.
 * - A git repository whose root a native command reads before the agent opens.
 * - One repository for each agent, so that the session picker of one directory lists one agent alone.
 * - The directory where the Worker stored a session, which an agent reopens.
 *
 * `reason` states which need applies and why the rule of the provider cannot meet it, so each exception is visible
 * at its call and a search for this function lists every exception.
 */
export function deliberateWorkingDir(directory: string, reason: string): ProviderWorkingDir {
  if (!isAbsolute(directory))
    throw new Error(`A deliberate working directory must be an absolute path, not ${JSON.stringify(directory)}.`)
  if (reason.trim() === '')
    throw new Error(`The deliberate working directory ${directory} must state why no rule of a provider applies.`)
  return brand(directory)
}
