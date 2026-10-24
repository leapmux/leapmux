import type { DroidModelProvider } from '../helpers/droidEnvironment'
import type { PrivateWorkerSetup } from '../helpers/privateNativeWorkspace'
import { createDroidEnvironment } from '../helpers/droidEnvironment'
import { createMockAgentEnvironment, DROID_MOCK_MODEL_IDS, MOCK_MODELS, MODEL_KEY } from '../helpers/mockAgentEnvironment'

/** Configure two independent native model APIs in this test's private Factory home. */
export async function prepareDroidAttachments(
  runDirectory: string,
  mockModelUrl: string,
  primaryProvider: DroidModelProvider,
  alternateProvider: DroidModelProvider = 'generic-chat-completion-api',
): Promise<PrivateWorkerSetup<undefined>> {
  const environment = await createMockAgentEnvironment(runDirectory, mockModelUrl)
  const nativeEnv = createDroidEnvironment({
    homeDir: environment.homeDir,
    baseURL: `${new URL(mockModelUrl).origin}/v1`,
    modelKey: MODEL_KEY,
    primary: { handle: DROID_MOCK_MODEL_IDS.primary, model: MOCK_MODELS.droid, provider: primaryProvider },
    alternate: { handle: DROID_MOCK_MODEL_IDS.alternate, model: MOCK_MODELS.droidAlt, provider: alternateProvider },
  })
  return { agentEnv: { ...environment.env, ...nativeEnv }, setup: undefined }
}
