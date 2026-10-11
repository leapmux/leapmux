import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { GOOSE_MODE } from '../../../src/generated/contracts/goose-protocol'
import { gooseTest } from '../goose-fixtures'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow } from '../helpers/nativePermission'
import { currentNativeAgent, expectNativeOptionValue } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, goosePermissionJudgmentToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { savedControlAnswer } from '../helpers/ui'
import { exerciseGoosePermissionRemoval } from './permissionScenario'

/**
 * Prepare a new Goose session to ask about each call, and return its working directory.
 *
 * A new session starts in Smart Approve. Smart Approve asks Goose's permission-safety classifier about each tool call,
 * and the rule makes the classifier list no call as read-only, so each call raises a permission request.
 */
async function askingSmartSession(context: ManagedNativeScenarioContext): Promise<string> {
  const workingDir = (await currentNativeAgent(context)).workingDir
  if (!workingDir)
    throw new Error('the Goose workspace has no working directory')
  await expectNativeOptionValue(context, 'permissionMode', GOOSE_MODE.SmartApprove)
  await context.modelScript.rule({ name: `goose-permission-judge-${uniqueMarker()}`, when: { system: 'permission-safety classifier' }, respond: { toolCalls: [goosePermissionJudgmentToolCall('goose-permission-judge', [])] } })
  return workingDir
}

gooseTest('smart mode asks before a removal and auto mode runs it', async ({ native }) => {
  await exerciseGoosePermissionRemoval(native)
})

gooseTest('runs a removal after the reader allows it in smart mode', async ({ native }) => {
  const workingDir = await askingSmartSession(native)
  const marker = join(workingDir, 'goose-allow-marker.txt')
  writeFileSync(marker, 'remove this file\n')
  await exerciseNativePermissionDecision(native, {
    // The command computes its output, so only a run prints it.
    toolCall: bashToolCall(native.provider, 'goose-smart-allow', 'rm -f goose-allow-marker.txt && printf "goose-allow-%s" "$((40 + 2))"'),
    decision: 'allow',
    beforeDecision: async (banner) => {
      await expect(banner).toContainText('goose-allow-marker.txt')
      expect(existsSync(marker)).toBe(true)
    },
    nativeProof: (request) => {
      expect(existsSync(marker)).toBe(false)
      expect(nativeToolResult(request, 'goose-smart-allow')).toContain('goose-allow-42')
    },
    // Goose names each option after its kind, so the saved row reads the word of the kind.
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow once'),
  })
})

// The ACP reply selects an option, and an option carries no text. The reason follows as the reader's next message.
gooseTest('sends the reader\'s typed refusal reason as the next message', async ({ native }) => {
  const workingDir = await askingSmartSession(native)
  const marker = join(workingDir, 'goose-reason-marker.txt')
  writeFileSync(marker, 'keep this file\n')
  await exerciseNativePermissionReason(native, {
    toolCall: bashToolCall(native.provider, 'goose-smart-reason', 'rm -f goose-reason-marker.txt'),
    route: 'next-message',
    beforeDecision: banner => expect(banner).toContainText('goose-reason-marker.txt'),
    expectNotRun: () => expect(existsSync(marker)).toBe(true),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Reject'),
  })
})

// Goose's request offers allow_always, and Goose keeps the answer for the tool, so a later call runs with no request.
gooseTest('an always answer covers the same command in the next turn', async ({ native }) => {
  const home = native.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The always scenario requires the isolated HOME of Goose.')
  const workingDir = await askingSmartSession(native)
  const file = join(workingDir, 'goose-always.txt')
  // Each run appends the marker, so the file states how many runs happened.
  const command = `printf goose-always >> ${quotePosixShellArgument(file)}`
  await exerciseRememberedAllow(native, {
    scope: 'Always',
    firstCall: bashToolCall(native.provider, 'goose-always-first', command),
    secondCall: bashToolCall(native.provider, 'goose-always-second', command),
    beforeDecision: () => expect(existsSync(file)).toBe(false),
    firstProof: () => expect(readFileSync(file, 'utf8')).toBe('goose-always'),
    secondProof: () => expect(readFileSync(file, 'utf8')).toBe('goose-alwaysgoose-always'),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow always'),
    // Goose keeps the answer for the shell tool in its own configuration. Every later Goose spec of the run reads it,
    // and the Smart scenario requires a request for the same tool.
    ruleFiles: [join(home, '.goose', 'config', 'permission.yaml')],
  })
})
