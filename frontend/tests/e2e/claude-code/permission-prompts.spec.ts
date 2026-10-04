import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'

for (const decision of ['allow', 'deny'] as const) {
  claudeTest(`returns the actual native ${decision} decision before a protected file changes`, async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
    void authenticatedClaudeWorkspace
    await chooseSettingsOption(page, 'permissionMode-default')
    await waitForSettingsIdle(page)
    const file = join(createTestDirectory('claude-native-permission-'), 'protected.txt')
    const callId = `native-permission-${decision}`
    const command = `printf 'PERMISSION%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
    await exerciseNativePermissionDecision({ page, modelScript, provider: AgentProvider.CLAUDE_CODE }, {
      toolCall: bashToolCall(AgentProvider.CLAUDE_CODE, callId, command),
      decision,
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      nativeProof: (request) => {
        const result = nativeToolResult(request, callId)
        if (decision === 'allow') {
          expect(result).toContain('PERMISSION42')
          expect(readFileSync(file, 'utf8')).toBe('PERMISSION42\n')
        }
        else {
          expect(result).toMatch(/denied|permission|rejected/i)
          expect(existsSync(file)).toBe(false)
        }
      },
    })
  })
}
