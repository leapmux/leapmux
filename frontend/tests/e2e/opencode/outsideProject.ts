import type { NativePermissionOperationPlan } from '../helpers/nativePermission'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { ProviderWorkingDir } from '../helpers/providerWorkingDir'
import { existsSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { nativeToolOutcome } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { deliberateWorkingDir } from '../helpers/providerWorkingDir'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { printfMarkerCommand } from '../helpers/shellArguments'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { createGitRepo } from '../helpers/worktree'
import { nativeContext, OPENCODE_AGENT } from './scenarios'

/**
 * Write a probe file that holds `content` into a new directory of the run whose name starts with `prefix`, and make a
 * working directory beside it. Return both. The name of the probe file is `<prefix>probe.txt`.
 *
 * OpenCode checks external-directory access for the shell's working directory and parsed directory changes.
 * The working directory is a private repository root. Commands enter its parent before touching the probe file,
 * so the native external-directory check runs before the file can change.
 */
export function projectBesideProbe(prefix: string, content: string): { workingDir: ProviderWorkingDir, file: string } {
  const parent = createTestDirectory(prefix)
  const file = join(parent, `${prefix}probe.txt`)
  writeFileSync(file, content)
  const workingDir = deliberateWorkingDir(
    createGitRepo(parent, 'repo'),
    'The command enters the parent of this private repository. The permission test profile requires approval before the command touches the probe file.',
  )
  return { workingDir, file }
}

/** Run a scenario with Build configured to ask before external-directory access. */
export async function withOutsideProjectAgent(
  fixtures: NativeContextFixtures,
  workingDir: ProviderWorkingDir,
  use: (context: ManagedNativeScenarioContext) => Promise<void>,
): Promise<void> {
  const configRoot = fixtures.leapmuxServer.agentEnv?.XDG_CONFIG_HOME
  if (!configRoot)
    throw new Error('The OpenCode permission fixture requires its private configuration directory.')
  // Build starts with a wildcard allow rule. These scenarios require an explicit ask rule for external directories.
  await withNativeConfigurationFile({
    path: join(configRoot, 'opencode', 'opencode.json'),
    content: JSON.stringify({
      agents: {
        build: {
          permissions: [
            { action: 'external_directory', resource: '*', effect: 'ask' },
          ],
        },
      },
    }),
    runDir: getGlobalState().tmpDir,
  }, async () => {
    await openProviderAgent(fixtures.leapmuxServer, fixtures.workspaceId, OPENCODE_AGENT, { workingDir })
    await openWorkspace(fixtures.page, fixtures.workspaceId)
    await use(await nativeContext(fixtures))
  })
}

/**
 * A shell command that removes `file` and prints `<outputPrefix>42`. The working directory of the agent is the
 * repository of {@link projectBesideProbe}. Enter its parent explicitly so the native external-directory check includes it.
 * The test profile requires approval before the removal. The printed marker proves that the command ran.
 */
export function outsideRemovalCommand(file: string, outputPrefix: string): string {
  const name = basename(file)
  // The path stays a bare word, as in the permission specs that prove the ask, so the name must need no quote.
  if (!/^[\w.-]+$/.test(name))
    throw new Error(`The outside probe needs a file name without shell metacharacters, not ${JSON.stringify(name)}.`)
  return `cd .. && rm ${name} && ${printfMarkerCommand(outputPrefix, 42)}`
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
