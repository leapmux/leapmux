import type { NativePermissionOperationPlan } from '../helpers/nativePermission'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'

/** MiMo's native deletion request supplies the actual permission control for these limit proofs. */
export async function createMiMoControlDeletion(context: ManagedNativeScenarioContext, purpose: 'editor' | 'workspace-trust'): Promise<NativePermissionOperationPlan> {
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native MiMo control requires a working directory.')
  const file = join(agent.workingDir, `native-${purpose}-control.txt`)
  writeFileSync(file, 'Native control scratch.\n')
  const callId = `native-${purpose}-permission`
  const command = `rm -f ${quotePosixShellArgument(file)}; printf 'NATIVECONTROL%s\\n' "$((40 + 2))"`
  return {
    toolCall: bashToolCall(context.provider, callId, command),
    beforeDecision: () => {
      expect(existsSync(file)).toBe(true)
    },
    nativeProof: (request) => {
      expect(nativeModelContextText(request)).toContain('NATIVECONTROL42')
      expect(nativeToolResult(request, callId)).toContain('NATIVECONTROL42')
      expect(existsSync(file)).toBe(false)
    },
  }
}
