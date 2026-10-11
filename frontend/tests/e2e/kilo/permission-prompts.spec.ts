import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { createNativePermissionFileWrite, exerciseNativePermissionReason, exerciseNativePermissionWrite, exerciseRememberedAllow } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { savedControlAnswer } from '../helpers/ui'
import { kiloTest } from '../kilo-fixtures'
import { exerciseOpenCodeFamilyDenial } from '../opencode/permissionDenial'

kiloTest('keeps actual file bytes unchanged until the native Allow decision', async ({ native }) => {
  await exerciseNativePermissionWrite(native, {
    // The saved row reads the name of Kilo's own `once` option.
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow once'),
  })
})

kiloTest('keeps exact file bytes after a native Deny decision', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const fileName = 'native-denied-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  // Kilo asks its bash permission for this command, because its default bash rule is ask and no allow rule covers node.
  const operation = await createNativePermissionFileWrite(native, { fileName, callId: 'kilo-denied-write', outputPrefix: 'UNAPPROVED_WRITE', initialContent })
  await exerciseOpenCodeFamilyDenial(native, {
    toolCall: operation.toolCall,
    prompt: 'Run the scripted permission probe.',
    expectUnchanged: () => expect(readFileSync(file, 'utf8')).toBe(initialContent),
  })
})

// The ACP reply selects an option, and an option carries no text. Kilo ends the turn after a refusal, so the reason
// follows as the reader's next message, which opens a turn of its own.
kiloTest('sends the reader\'s typed refusal reason as the next message', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const file = join(agent.workingDir, 'native-reason-write.txt')
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const operation = await createNativePermissionFileWrite(native, { fileName: 'native-reason-write.txt', callId: 'kilo-reason-write', outputPrefix: 'UNAPPROVED_WRITE', initialContent })
  await exerciseNativePermissionReason(native, {
    toolCall: operation.toolCall,
    route: 'next-message',
    afterRefusal: 'ends',
    expectNotRun: () => expect(readFileSync(file, 'utf8')).toBe(initialContent),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Reject'),
  })
})

// Kilo keeps an always answer for the command pattern of the call, so the same command later runs with no request.
kiloTest('an always answer covers the same command in the next turn', async ({ native }) => {
  const home = native.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The always scenario requires the isolated HOME of Kilo.')
  const file = join((await currentNativeAgent(native)).workingDir, 'native-always.txt')
  // Each run appends the marker, so the file states how many runs happened.
  const command = `printf kilo-always >> ${quotePosixShellArgument(file)}`
  await exerciseRememberedAllow(native, {
    scope: 'Always',
    firstCall: bashToolCall(native.provider, 'kilo-always-first', command),
    secondCall: bashToolCall(native.provider, 'kilo-always-second', command),
    beforeDecision: () => expect(existsSync(file)).toBe(false),
    firstProof: () => expect(readFileSync(file, 'utf8')).toBe('kilo-always'),
    secondProof: () => expect(readFileSync(file, 'utf8')).toBe('kilo-alwayskilo-always'),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Always allow'),
    // Kilo also writes the rule to its global configuration, which every later Kilo spec of the run reads.
    ruleFiles: [join(home, '.config', 'kilo', 'kilo.jsonc')],
  })
})
