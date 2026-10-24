import type { PrivateWorkerSetup } from '../helpers/privateNativeWorkspace'
import { appendFileSync } from 'node:fs'
import { join } from 'node:path'
import { createMockAgentEnvironment } from '../helpers/mockAgentEnvironment'

export const REASONIX_SMART_ASK_COMMAND = 'printf reasonix-smart-ask > reasonix-smart-ask.txt'
export const REASONIX_SMART_DENY_COMMAND = 'printf reasonix-smart-denied > reasonix-smart-denied.txt'

/** Keep native ask and deny rules inside this test's private Reasonix configuration. */
export async function prepareReasonixPermissionRules(runDirectory: string, mockModelUrl: string): Promise<PrivateWorkerSetup<undefined>> {
  const environment = await createMockAgentEnvironment(runDirectory, mockModelUrl)
  appendFileSync(join(environment.homeDir, '.reasonix', 'config.toml'), `
[permissions]
ask = [${JSON.stringify(`Bash=${REASONIX_SMART_ASK_COMMAND}`)}]
deny = [${JSON.stringify(`Bash=${REASONIX_SMART_DENY_COMMAND}`)}]
`)
  return { agentEnv: environment.env, setup: undefined }
}
