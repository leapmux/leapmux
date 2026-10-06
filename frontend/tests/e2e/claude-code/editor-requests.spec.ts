import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { claudeExtractControl } from '../../../src/components/chat/providers/claude/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'

claudeTest('classifies an actual native permission and confirms the absent editor-requests route', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  const file = join(createTestDirectory('native-editor-'), 'native-control.txt')
  const callId = 'native-control-permission'
  const command = `printf 'NATIVECONTROL%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'editor',
    classify: claudeExtractControl,
    relatedProof: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(AgentProvider.CLAUDE_CODE, callId, command),
      decision: 'allow',
      beforeDecision: async (banner) => {
        expect(existsSync(file)).toBe(false)
        await beforeDecision(banner)
      },
      nativeProof: (request) => {
        expect(nativeToolResult(request, callId)).toContain('NATIVECONTROL42')
        expect(readFileSync(file, 'utf8')).toBe('NATIVECONTROL42\n')
      },
    }),
  })
})
