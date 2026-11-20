import { Buffer } from 'node:buffer'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow, expectSavedRefusalFeedback } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, savedControlAnswer, sendMessage, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

for (const decision of ['allow', 'deny'] as const) {
  claudeTest(`returns the actual native ${decision} decision before a protected file changes`, async ({ native }) => {
    await chooseSettingsOption(native.page, 'permissionMode-default')
    await waitForSettingsIdle(native.page)
    const file = join(createTestDirectory('claude-native-permission-'), 'protected.txt')
    const callId = `native-permission-${decision}`
    const command = `printf 'PERMISSION%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, callId, command),
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
      // The saved answer states the option that the decision chose.
      viewProof: () => expect(savedControlAnswer(native.page)).toHaveText(decision === 'allow'
        ? 'Allow once'
        : 'Deny'),
    })
  })
}

claudeTest('records the verified choice after answering a permission prompt through raw rpc', async ({ native, authenticatedWorkspace, leapmuxServer }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const file = join(createTestDirectory('claude-rpc-permission-'), 'approved.txt')
  const callId = 'native-permission-rpc'
  const command = `printf PERMISSION42 > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  const start = await native.modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, callId, command),
    ],
  }, { text: 'The approved command ran.' })
  await sendMessage(native.page, native.modelScript.prompt('Run the scripted permission probe.'))
  await waitForControlBanner(native.page)
  expect(existsSync(file)).toBe(false)

  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const target = { agent_id: authenticatedWorkspace.agentId }
  const worker = ['--worker', leapmuxServer.workerId]
  const listed = await runCLI(cli, [
    'rpc',
    'WorkerAgentService/ListControlRequests',
    JSON.stringify(target),
    ...worker,
  ]) as { requests: Array<{ request_id: string, claim_token: string }> }
  expect(listed.requests).toHaveLength(1)
  const [pending] = listed.requests
  if (!pending)
    throw new Error('The Worker returned no pending permission request.')
  const { request_id: requestId, claim_token: claimToken } = pending
  const content = Buffer.from(JSON.stringify({
    type: 'control_response',
    response: {
      request_id: requestId,
      subtype: 'success',
      response: { behavior: 'allow', choice: 'once' },
    },
  })).toString('base64')
  const answered = await runCLI(cli, [
    'rpc',
    'WorkerAgentService/AnswerControl',
    JSON.stringify({
      ...target,
      request_id: requestId,
      claim_token: claimToken,
      native_response: { content },
    }),
    ...worker,
  ])
  expect(answered).toMatchObject({ state: 'CONTROL_RESPONSE_STATE_COMPLETED' })
  await native.modelScript.waitForSteps(start + 2)
  const result = nativeToolResult(await native.modelScript.requestAt(start + 1), callId)
  expect(result).toContain('PERMISSION42')
  expect(readFileSync(file, 'utf8')).toBe('PERMISSION42')
  await expect(savedControlAnswer(native.page)).toHaveText('Allow once')
})

// The reason rides in the `message` of Claude's own permission answer, and Claude hands it to the model as the result
// of the refused call.
claudeTest('hands the reader\'s typed refusal reason to the model as the result of the call', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const file = join(createTestDirectory('claude-native-permission-'), 'refused.txt')
  await exerciseNativePermissionReason(native, {
    toolCall: bashToolCall(native.provider, 'native-permission-reason', `printf refused > ${quotePosixShellArgument(file)}`),
    route: 'native-reply',
    expectNotRun: () => expect(existsSync(file)).toBe(false),
    viewProof: reason => expectSavedRefusalFeedback(native.page, reason),
  })
})

// The Allow scope group offers the session tier: the answer carries the
// `updatedPermissions` grant Claude Code keeps in memory for the session -- one
// allow rule for the whole Bash tool. A LATER command of the same tool must
// raise no banner at any time, which also proves the rule is tool-wide rather
// than command-typed. A session destination writes no rule file.
claudeTest('a session allow covers the tool for the rest of the session', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const dir = createTestDirectory('claude-native-session-')
  const firstFile = join(dir, 'first.txt')
  const secondFile = join(dir, 'second.txt')
  await exerciseRememberedAllow(native, {
    scope: 'Session',
    firstCall: bashToolCall(native.provider, 'native-session-first', `printf one > ${quotePosixShellArgument(firstFile)}`),
    secondCall: bashToolCall(native.provider, 'native-session-second', `printf two > ${quotePosixShellArgument(secondFile)}`),
    beforeDecision: () => expect(existsSync(firstFile)).toBe(false),
    firstProof: () => expect(readFileSync(firstFile, 'utf8')).toBe('one'),
    secondProof: () => expect(readFileSync(secondFile, 'utf8')).toBe('two'),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow for this session'),
  })
})
