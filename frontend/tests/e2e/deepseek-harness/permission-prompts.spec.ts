import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { deepseekHarnessEscalatedBashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { savedControlAnswer } from '../helpers/ui'
import { nativeContext } from './scenarios'

/** The justification that each scripted escalation states. */
const ESCALATION_REASON = 'Allow this exact isolated command to use the wider native sandbox.'

deepseekHarnessTest('allows or denies real native escalation before the command changes a file', async ({ askingDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDeepseekHarnessWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  // The saved rows of the session, in order: each one reads the native option that its decision selected.
  const saved: string[] = []
  for (const decision of ['deny', 'allow'] as const) {
    const path = join(agent.workingDir, `native-escalation-${decision}.txt`)
    const id = `native-escalation-${decision}`
    const command = `printf 'ESCALATION%s\n' "$((21 * 2))" > ${quotePosixShellArgument(path)}; cat ${quotePosixShellArgument(path)}`
    saved.push(decision === 'allow' ? 'Allow once' : 'Deny')
    const expectedSaved = [...saved]
    await exerciseNativePermissionDecision(context, {
      toolCall: deepseekHarnessEscalatedBashToolCall(id, command, ESCALATION_REASON),
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
      viewProof: () => expect(savedControlAnswer(page)).toHaveText(expectedSaved),
    })
  }
})

// The native approval reply carries no reason. The reason follows as the reader's next message, which opens a turn of
// its own after the refused turn.
deepseekHarnessTest('sends the reader\'s typed refusal reason as the next message', async ({ askingDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDeepseekHarnessWorkspace.workspaceId })
  const path = join((await currentNativeAgent(context)).workingDir, 'native-escalation-reason.txt')
  await exerciseNativePermissionReason(context, {
    toolCall: deepseekHarnessEscalatedBashToolCall('native-escalation-reason', `printf refused > ${quotePosixShellArgument(path)}`, ESCALATION_REASON),
    route: 'next-message',
    expectNotRun: () => expect(existsSync(path)).toBe(false),
    // The saved row states the decision alone, because the reason is the row of the next message.
    viewProof: () => expect(savedControlAnswer(page)).toHaveText('Deny'),
  })
})
