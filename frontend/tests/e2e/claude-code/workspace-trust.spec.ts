import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { claudeExtractControl } from '../../../src/components/chat/providers/claude/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { writeNativeProjectHook } from '../helpers/nativeProjectHook'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit, outsideFileWriteOperation } from '../helpers/nativeWorkspaceTrustLimit'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'

claudeTest('classifies an actual native permission and confirms the absent workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, {
    askOption: 'permissionMode-default',
    classify: claudeExtractControl,
    operation: () => outsideFileWriteOperation((callId, command) => bashToolCall(AgentProvider.CLAUDE_CODE, callId, command)),
  })
})

claudeTest('loads the actual project startup hook without a native trust barrier', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, {
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const hook = writeNativeProjectHook(directory, marker)
        const configuration = join(directory, '.claude', 'settings.json')
        mkdirSync(dirname(configuration), { recursive: true })
        writeFileSync(configuration, JSON.stringify({ hooks: { SessionStart: [{ hooks: [{ type: 'command', command: `${quotePosixShellArgument(process.execPath)} ${quotePosixShellArgument(hook.scriptPath)}` }] }] } }))
      },
      prove: async (privateContext, { directory, marker }) => {
        await sendNativeAnswer(privateContext, 'Reply once from this project.', 'The native project startup turn completed.')
        const receipt = join(directory, 'native-project-hook-receipt.json')
        expect(existsSync(receipt)).toBe(true)
        const actual: unknown = JSON.parse(readFileSync(receipt, 'utf8'))
        expect(actual).toMatchObject({ marker, workingDirectory: directory, input: { hook_event_name: 'SessionStart', cwd: directory } })
      },
    },
  })
})
