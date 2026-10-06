import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { deepseekHarnessEscalatedBashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { nativeContext } from './scenarios'

deepseekHarnessTest('allows or denies real native escalation before the command changes a file', async ({ askingDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDeepseekHarnessWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  for (const decision of ['deny', 'allow'] as const) {
    const path = join(agent.workingDir, `native-escalation-${decision}.txt`)
    const id = `native-escalation-${decision}`
    const command = `printf 'ESCALATION%s\n' "$((21 * 2))" > ${quotePosixShellArgument(path)}; cat ${quotePosixShellArgument(path)}`
    await exerciseNativePermissionDecision(context, {
      toolCall: deepseekHarnessEscalatedBashToolCall(id, command, 'Allow this exact isolated command to use the wider native sandbox.'),
      decision,
      beforeDecision: () => { expect(existsSync(path)).toBe(false) },
      nativeProof: async (request) => {
        const result = nativeToolResult(request, id)
        if (decision === 'allow') {
          expect(readFileSync(path, 'utf8')).toBe('ESCALATION42\n')
          expect(result).toContain('ESCALATION42')
        }
        else {
          expect(existsSync(path)).toBe(false)
          expect(result).toMatch(/denied|rejected|not approved/i)
        }
      },
    })
  }
})
