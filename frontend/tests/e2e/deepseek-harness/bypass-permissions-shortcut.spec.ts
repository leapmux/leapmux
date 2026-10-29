import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { DEEPSEEK_HARNESS_OPTION, DEEPSEEK_HARNESS_PERMISSION_PRESET } from '../../../src/generated/contracts/deepseek-harness-protocol'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { deepseekHarnessEscalatedBashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { nativeContext } from './scenarios'

deepseekHarnessTest('selects the native full-access preset and preserves it after reload', async ({ askingDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingDeepseekHarnessWorkspace.workspaceId,
  })
  await exerciseBypassPermissions(context, {
    settingsProof: agent => expect(nativeOptionValue(agent, DEEPSEEK_HARNESS_OPTION.Permissions)).toBe(DEEPSEEK_HARNESS_PERMISSION_PRESET.DangerFullAccess),
  })
})

deepseekHarnessTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ askingDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingDeepseekHarnessWorkspace.workspaceId,
  })
  const operation = (index: number) => {
    const path = join(askingDeepseekHarnessWorkspace.workingDir, `mid-bypass-${index}.txt`)
    const id = `mid-bypass-${index}`
    const command = `printf 'ESCALATION%s\\n' "$((21 * 2))" > ${quotePosixShellArgument(path)}; cat ${quotePosixShellArgument(path)}`
    return {
      toolCall: deepseekHarnessEscalatedBashToolCall(id, command, 'Run this isolated command outside the native sandbox.'),
      beforeDecision: () => { expect(existsSync(path)).toBe(false) },
      nativeProof: (request: Parameters<typeof nativeToolResult>[0]) => {
        expect(nativeToolResult(request, id)).toContain('ESCALATION42')
        expect(readFileSync(path, 'utf8')).toBe('ESCALATION42\n')
      },
    }
  }
  await exerciseMidTurnBypass(native, [
    operation(0),
    operation(1),
  ])
})
