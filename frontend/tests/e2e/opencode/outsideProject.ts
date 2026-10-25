import type { NativePermissionOperationPlan } from '../helpers/nativePermission'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { ProviderWorkingDir } from '../helpers/providerWorkingDir'
import { existsSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { nativeToolOutcome } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { deliberateWorkingDir } from '../helpers/providerWorkingDir'
import { createTestDirectory } from '../helpers/runDirectory'
import { printfMarkerCommand } from '../helpers/shellArguments'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { createGitRepo } from '../helpers/worktree'
import { nativeContext, OPENCODE_AGENT } from './scenarios'

/**
 * Write a probe file that holds `content` into a new directory of the run whose name starts with `prefix`, and make a
 * working directory beside it. Return both. The name of the probe file is `<prefix>probe.txt`.
 *
 * OpenCode treats the whole git worktree of its directory as its project, and asks its `external_directory`
 * permission only for a path outside that project. A plain directory of the run lies in no git repository, so it is
 * no project root of its own. So the working directory is the root of a repository of its own, and the probe file sits
 * in its parent, outside the project.
 */
export function projectBesideProbe(prefix: string, content: string): { workingDir: ProviderWorkingDir, file: string } {
  const parent = createTestDirectory(prefix)
  const file = join(parent, `${prefix}probe.txt`)
  writeFileSync(file, content)
  const workingDir = deliberateWorkingDir(
    createGitRepo(parent, 'repo'),
    'OpenCode asks its external_directory permission only for a path outside the git worktree of its directory, so the directory is a repository of its own and the probe file sits in its parent.',
  )
  return { workingDir, file }
}

/** Open an OpenCode agent in `workingDir`, show its workspace, and return the scenario context of the agent. */
export async function openOutsideProjectAgent(fixtures: NativeContextFixtures, workingDir: ProviderWorkingDir): Promise<ManagedNativeScenarioContext> {
  await openProviderAgent(fixtures.leapmuxServer, fixtures.workspaceId, OPENCODE_AGENT, { workingDir })
  await openWorkspace(fixtures.page, fixtures.workspaceId)
  return nativeContext(fixtures)
}

/**
 * A shell command that removes `file` and prints `<outputPrefix>42`. The working directory of the agent is the
 * repository of {@link projectBesideProbe}, and `file` sits in its parent, so OpenCode asks its `external_directory`
 * permission before the command runs. The printed marker proves that the command ran.
 */
export function outsideRemovalCommand(file: string, outputPrefix: string): string {
  const name = basename(file)
  // The path stays a bare word, as in the permission specs that prove the ask, so the name must need no quote.
  if (!/^[\w.-]+$/.test(name))
    throw new Error(`The outside probe needs a file name without shell metacharacters, not ${JSON.stringify(name)}.`)
  return `rm ../${name}; ${printfMarkerCommand(outputPrefix, 42)}`
}

/** The shell call of {@link outsideRemovalCommand}, with its checks before the decision and after the call. */
export function outsideRemoval(context: ManagedNativeScenarioContext, file: string, callId: string, outputPrefix: string): NativePermissionOperationPlan {
  return {
    toolCall: bashToolCall(context.provider, callId, outsideRemovalCommand(file, outputPrefix)),
    beforeDecision: () => expect(existsSync(file)).toBe(true),
    nativeProof: async (request) => {
      expect(existsSync(file)).toBe(false)
      expect((await nativeToolOutcome(context, request, callId)).text).toContain(`${outputPrefix}42`)
    },
  }
}
