import { existsSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { CLIENT_APPROVAL_OPTION } from '../../../src/generated/contracts/client-approval'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { expectPermissionShortcuts, waitForNativeSettingsHydrated } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'
import { openOutsideProjectAgent, outsideRemoval, outsideRemovalCommand, projectBesideProbe } from './outsideProject'

// OpenCode allows a shell command inside its project, and asks its external_directory permission for a path outside
// it. Each command here also removes a file beside the project, so it asks unless Bypass answers the request.
opencodeTest('offers Bypass and runs an outside-directory command without a prompt before and after reload', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file } = projectBesideProbe('opencode-bypass-', 'remove the first file')
  const context = await openOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir)
  await waitForNativeSettingsHydrated(page)
  await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
  await exerciseBypassPermissions(context, {
    settingsProof: agent => expect(nativeOptionValue(agent, CLIENT_APPROVAL_OPTION.AutoApprove)).toBe('true'),
    commandProof: (target, pass) => {
      const outside = pass === 1
        ? file
        : join(dirname(file), 'opencode-bypass-second-probe.txt')
      if (pass !== 1)
        writeFileSync(outside, 'remove the second file')
      const output = `OPENCODE_BYPASS_${pass}_`
      return {
        command: `rm -r ${quotePosixShellArgument(target)}; ${outsideRemovalCommand(outside, output)}`,
        resultProof: (outcome) => {
          expect(outcome.text).toContain(`${output}42`)
          expect(existsSync(outside), 'the outside file goes only with an approved request').toBe(false)
        },
      }
    },
  })
})

opencodeTest('approves a pending outside-directory command and the next one when Bypass changes during a turn', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file } = projectBesideProbe('opencode-mid-bypass-', 'remove the first file')
  const second = join(dirname(file), 'opencode-mid-bypass-second-probe.txt')
  writeFileSync(second, 'remove the second file')
  const context = await openOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir)
  await exerciseMidTurnBypass(context, [
    outsideRemoval(context, file, 'opencode-mid-bypass-0', 'OPENCODE_MID_BYPASS_0_'),
    outsideRemoval(context, second, 'opencode-mid-bypass-1', 'OPENCODE_MID_BYPASS_1_'),
  ])
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/515.
