import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { assertPrivateNativeAncestor } from './nativeConfigurationFile'
import { currentNativeAgent, nativeToolOutcome } from './nativeScenario'
import { bashToolCall } from './providerToolCalls'
import { getGlobalState } from './server'
import { printfMarkerCommand, quotePosixShellArgument, uniqueMarker } from './shellArguments'

/** Prepare a private file whose removal requires a native permission decision. */
export async function createNativePermissionRemoval(context: ManagedNativeScenarioContext): Promise<NativePermissionOperationPlan> {
  const marker = uniqueMarker('REMOVE')
  const path = join((await currentNativeAgent(context)).workingDir, `${marker}.txt`)
  assertPrivateNativeAncestor(path, getGlobalState().tmpDir, { refuseSymlink: true })
  const content = 'Keep this file until the tool runs.\n'
  writeFileSync(path, content, { flag: 'wx', mode: 0o600 })
  return {
    toolCall: bashToolCall(context.provider, marker, `rm -rf ${quotePosixShellArgument(path)} && ${printfMarkerCommand(marker, 42)}`),
    beforeDecision: () => { expect(readFileSync(path, 'utf8')).toBe(content) },
    nativeProof: async (request) => {
      expect((await nativeToolOutcome(context, request, marker)).text, 'the native command must report its removal marker').toContain(`${marker}42`)
      expect(existsSync(path), 'the native command must remove its private file').toBe(false)
    },
  }
}
