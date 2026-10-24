import type { PrivateWorkerSetup } from '../helpers/privateNativeWorkspace'
import { createMockAgentEnvironment, MOCK_MODELS, MODEL_KEY, REASONIX_ALT_PROVIDER_ID, REASONIX_PLAIN_PROVIDER_ID } from '../helpers/mockAgentEnvironment'
import { createReasonixEnvironment } from '../helpers/reasonixEnvironment'

export const REASONIX_COMPACTION_CONTEXT_WINDOW = 50000

/** Give automatic compaction a private native context window and scripted model endpoint. */
export async function prepareCompactionReasonix(runDirectory: string, mockModelUrl: string): Promise<PrivateWorkerSetup<undefined>> {
  const environment = await createMockAgentEnvironment(runDirectory, mockModelUrl)
  const nativeEnv = createReasonixEnvironment({
    homeDir: environment.homeDir,
    baseURL: `${new URL(mockModelUrl).origin}/v1`,
    modelKey: MODEL_KEY,
    contextWindow: REASONIX_COMPACTION_CONTEXT_WINDOW,
    modelID: MOCK_MODELS.deepseek,
    alternateProviderID: REASONIX_ALT_PROVIDER_ID,
    alternateModelID: MOCK_MODELS.pi,
    plainProviderID: REASONIX_PLAIN_PROVIDER_ID,
    plainModelID: MOCK_MODELS.zai,
  })
  return { agentEnv: { ...environment.env, ...nativeEnv }, setup: undefined }
}
