import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { acpExtractControl } from '../../../src/components/chat/providers/acp/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('offers an actual native permission without an agent question sequence', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native question limit requires a private working directory.')
  const file = join(agent.workingDir, 'native-question-permission.txt')
  const command = `printf 'NATIVEQUESTION%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'question',
    classify: acpExtractControl,
    relatedProof: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(AgentProvider.REASONIX, 'native-question-permission', command),
      decision: 'allow',
      beforeDecision,
      nativeProof: (request) => {
        expect(readFileSync(file, 'utf8')).toBe('NATIVEQUESTION42\n')
        expect(nativeToolResult(request, 'native-question-permission')).toContain('NATIVEQUESTION42')
        const tools = nativeModelToolNames(request)
        expect(tools.length).toBeGreaterThan(0)
        expect(tools.some(tool => /^(?:ask_user|ask_user_question|AskUserQuestion|__human_input)$/.test(tool))).toBe(false)
      },
    }),
  })
})
