import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionReason } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { savedControlAnswer } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeRemovalPermission } from './permissionScenario'

zcodeTest('a risky command produces a permission banner that can be denied', async ({ native }) => {
  await exerciseZCodeRemovalPermission(native, { bypass: false, decision: 'deny' })
})

zcodeTest('an allow under the Unchanged pill runs the command and keeps the mode', async ({ native }) => {
  await exerciseZCodeRemovalPermission(native, { bypass: false, decision: 'allow' })
})

// The reason rides in the `reason` of the deny option's own response, and ZCode hands it to the model.
zcodeTest('a typed refusal reason reaches the model with the denial', async ({ native }) => {
  const file = join((await currentNativeAgent(native)).workingDir, 'zcode-reason.txt')
  const content = 'Keep the private permission fixture.\n'
  writeFileSync(file, content)
  await exerciseNativePermissionReason(native, {
    toolCall: bashToolCall(native.provider, 'zcode-reason-removal', `rm -rf ${quotePosixShellArgument(file)}`),
    route: 'native-reply',
    beforeDecision: banner => expect(banner).toContainText('zcode-reason.txt'),
    expectNotRun: () => expect(readFileSync(file, 'utf8')).toBe(content),
    // The saved row states the decision, and the reason that the native reply carried on a line of its own.
    viewProof: reason => expect(savedControlAnswer(native.page)).toHaveText(`Deny\n${reason}`),
  })
})
