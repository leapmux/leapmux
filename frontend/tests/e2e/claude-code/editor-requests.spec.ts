import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { claudeExtractControl } from '../../../src/components/chat/providers/claude/extractControl'
import { claudeTest } from '../claude-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

// Every Claude Code permission spec in the default mode writes outside the working directory, and nothing in the
// repository states that Claude Code asks for a command that writes inside it. The proof therefore keeps its own write.
claudeTest('classifies an actual native permission and confirms the absent editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const file = join(createTestDirectory('native-editor-'), 'native-control.txt')
  const callId = 'native-control-permission'
  const command = `printf 'NATIVECONTROL%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  await exerciseUnsupportedControlThroughPermission(native, {
    purpose: 'editor',
    classify: claudeExtractControl,
    operation: {
      toolCall: bashToolCall(native.provider, callId, command),
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      nativeProof: (request) => {
        expect(nativeToolResult(request, callId)).toContain('NATIVECONTROL42')
        expect(readFileSync(file, 'utf8')).toBe('NATIVECONTROL42\n')
      },
    },
  })
})
