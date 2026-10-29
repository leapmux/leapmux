import type { PrivateWorkerSetup } from '../helpers/privateNativeWorkspace'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { createMockAgentEnvironment, MOCK_PROVIDER_IDS, OH_MY_PI_PROFILE } from '../helpers/mockAgentEnvironment'
import { createOhMyPiEnvironment } from '../helpers/ohMyPiEnvironment'

const fastModel = 'gpt-5.5'
export const OH_MY_PI_FAST_MODEL_ID = `${MOCK_PROVIDER_IDS.ohMyPi}/${fastModel}`

/** Prepare a private native OpenAI-family model whose requests support service_tier. */
export async function prepareFastModeOhMyPi(runDirectory: string, mockModelUrl: string): Promise<PrivateWorkerSetup<undefined>> {
  const environment = await createMockAgentEnvironment(runDirectory, mockModelUrl)
  const origin = new URL(mockModelUrl).origin
  const nativeEnv = createOhMyPiEnvironment({
    homeDir: environment.homeDir,
    origin,
    baseURL: `${origin}/v1`,
    profile: OH_MY_PI_PROFILE,
    providerID: MOCK_PROVIDER_IDS.ohMyPi,
    modelID: fastModel,
    alternateModelID: 'gpt-5.4',
    modelName: 'GPT-5.5',
    alternateModelName: 'GPT-5.4',
    mcpEchoServer: writeMcpEchoServer(runDirectory),
  })
  return {
    agentEnv: {
      ...environment.env,
      ...nativeEnv,
      LEAPMUX_OHMYPI_DEFAULT_MODEL: OH_MY_PI_FAST_MODEL_ID,
    },
    setup: undefined,
  }
}
