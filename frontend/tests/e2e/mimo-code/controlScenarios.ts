import type { NativePermissionOperationPlan } from '../helpers/nativePermission'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { GatedOutput } from '../helpers/outputGate'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { createOutputGate } from '../helpers/outputGate'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { messageContents } from '../helpers/ui'

/**
 * MiMo's native deletion request supplies the actual permission control for these limit proofs.
 *
 * The command ends with a print, and the proof reads that print in the model's tool result.
 * MiMo Code 0.1.15 can lose the output of a command that exits right after it writes,
 * so an output gate holds the command until the live view shows the print.
 */
export async function createMiMoControlDeletion(context: ManagedNativeScenarioContext, purpose: 'editor' | 'workspace-trust'): Promise<NativePermissionOperationPlan & { outputGate: GatedOutput }> {
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native MiMo control requires a working directory.')
  const file = join(agent.workingDir, `native-${purpose}-control.txt`)
  writeFileSync(file, 'Native control scratch.\n')
  const callId = `native-${purpose}-permission`
  const gate = createOutputGate(agent.workingDir)
  const command = gate.hold(`rm -f ${quotePosixShellArgument(file)}; printf 'NATIVECONTROL%s\\n' "$((40 + 2))"`)
  return {
    toolCall: bashToolCall(context.provider, callId, command),
    outputGate: { gate, shown: () => expect(messageContents(context.page).filter({ hasText: 'NATIVECONTROL42' }).first(), 'the live view shows the output of the held command').toBeVisible() },
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
