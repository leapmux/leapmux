import type { NativeContextFixtures } from './nativeScenario'
import { join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { requireLoopbackHttpURL } from './agentEnvironmentInputs'
import { GEMINI_MODEL_ID, MOCK_PROVIDER_IDS } from './mockAgentEnvironment'
import { withNativeConfigurationFile } from './nativeConfigurationFile'
import { getGlobalState } from './server'

export const OPENCODE_AUDIO_MODEL_ALIAS = 'leapmux-audio-fixture'
export const OPENCODE_AUDIO_MODEL_ID = `${MOCK_PROVIDER_IDS.openCode}/${OPENCODE_AUDIO_MODEL_ALIAS}`

function requireAudioFixtureEndpoint(baseURL: unknown): URL {
  if (typeof baseURL !== 'string')
    throw new TypeError('The OpenCode audio fixture requires its private mock endpoint.')
  const endpoint = requireLoopbackHttpURL(baseURL, 'The OpenCode audio fixture endpoint')
  if (endpoint.pathname !== '/v1')
    throw new Error('The OpenCode audio fixture requires the private OpenAI /v1 endpoint.')
  return endpoint
}

/** Add one Google model alias while preserving the private provider's endpoints, credentials and existing models. */
export function kiloAudioConfiguration(value: unknown): { configuration: Record<string, unknown>, textModelID: string } {
  const prefix = `${MOCK_PROVIDER_IDS.openCode}/`
  if (!isObject(value) || typeof value.model !== 'string' || !value.model.startsWith(prefix))
    throw new Error('The OpenCode audio fixture requires its private mock model.')
  const modelID = value.model.slice(prefix.length)
  const provider = isObject(value.provider)
    ? value.provider[MOCK_PROVIDER_IDS.openCode]
    : undefined
  const models = isObject(provider) && isObject(provider.models)
    ? provider.models
    : undefined
  const model = models?.[modelID]
  if (!models || !isObject(model) || !isObject(model.modalities) || !Array.isArray(model.modalities.input) || !model.modalities.input.every(part => typeof part === 'string'))
    throw new Error('The OpenCode audio fixture requires a private mock model with native input modalities.')
  if (model.id !== undefined && model.id !== modelID)
    throw new Error('The OpenCode audio fixture requires the existing private wire model.')
  if (Object.hasOwn(models, OPENCODE_AUDIO_MODEL_ALIAS))
    throw new Error('The private OpenCode audio model alias already exists.')
  const options = isObject(provider) && isObject(provider.options)
    ? provider.options
    : undefined
  const endpoint = requireAudioFixtureEndpoint(options?.baseURL)
  return {
    textModelID: value.model,
    configuration: {
      provider: {
        [MOCK_PROVIDER_IDS.openCode]: {
          models: {
            [OPENCODE_AUDIO_MODEL_ALIAS]: {
              ...structuredClone(model),
              id: GEMINI_MODEL_ID,
              name: 'LeapMux audio fixture',
              attachment: true,
              provider: { npm: '@ai-sdk/google', api: `${endpoint.origin}/v1beta` },
              modalities: {
                ...structuredClone(model.modalities),
                input: [
                  ...new Set([...model.modalities.input, 'audio']),
                ],
              },
            },
          },
        },
      },
    },
  }
}

/** Add a native Google model while keeping the private provider's credentials and text models. */
export function openCodeAudioConfiguration(value: unknown): { configuration: Record<string, unknown>, textModelID: string } {
  if (!isObject(value) || !isObject(value.model) || value.model.providerID !== MOCK_PROVIDER_IDS.openCode
    || typeof value.model.model !== 'string' || value.model.model === '') {
    throw new Error('The OpenCode audio fixture requires its private mock model.')
  }
  const modelID = value.model.model
  const provider = isObject(value.providers)
    ? value.providers[MOCK_PROVIDER_IDS.openCode]
    : undefined
  const models = isObject(provider) && isObject(provider.models)
    ? provider.models
    : undefined
  const model = models?.[modelID]
  if (!models || !isObject(model) || !isObject(model.capabilities) || !Array.isArray(model.capabilities.input)
    || !model.capabilities.input.every(part => typeof part === 'string')) {
    throw new Error('The OpenCode audio fixture requires a private mock model with native input capabilities.')
  }
  if (model.modelID !== undefined && model.modelID !== modelID)
    throw new Error('The OpenCode audio fixture requires the existing private wire model.')
  if (Object.hasOwn(models, OPENCODE_AUDIO_MODEL_ALIAS))
    throw new Error('The private OpenCode audio model alias already exists.')
  const settings = isObject(provider) && isObject(provider.settings)
    ? provider.settings
    : undefined
  const endpoint = requireAudioFixtureEndpoint(settings?.baseURL)
  return {
    textModelID: `${MOCK_PROVIDER_IDS.openCode}/${modelID}`,
    configuration: {
      providers: {
        [MOCK_PROVIDER_IDS.openCode]: {
          models: {
            [OPENCODE_AUDIO_MODEL_ALIAS]: {
              ...structuredClone(model),
              modelID: GEMINI_MODEL_ID,
              name: 'LeapMux audio fixture',
              package: '@opencode/ai/providers/google',
              settings: {
                ...(isObject(model.settings)
                  ? structuredClone(model.settings)
                  : {}),
                baseURL: `${endpoint.origin}/v1beta`,
              },
              capabilities: {
                ...structuredClone(model.capabilities),
                input: [
                  ...new Set([...model.capabilities.input, 'audio']),
                ],
              },
              variants: [],
            },
          },
        },
      },
    },
  }
}

/** Apply the member's private global model alias while project configuration remains disabled. */
export async function withOpenCodeAudioCatalog(
  server: NativeContextFixtures['leapmuxServer'],
  member: 'opencode' | 'kilo',
  use: (textModelID: string) => Promise<void>,
): Promise<void> {
  const keys = { opencode: 'OPENCODE_CONFIG_CONTENT', kilo: 'KILO_CONFIG_CONTENT' } as const
  const root = server.agentEnv?.XDG_CONFIG_HOME
  const inline = server.agentEnv?.[keys[member]]
  if (!root || !inline)
    throw new Error('The OpenCode family audio proof requires its private native configuration.')
  const value: unknown = JSON.parse(inline)
  const fixture = member === 'opencode'
    ? openCodeAudioConfiguration(value)
    : kiloAudioConfiguration(value)
  await withNativeConfigurationFile({
    path: join(root, member, 'opencode.json'),
    content: JSON.stringify(fixture.configuration),
    runDir: getGlobalState().tmpDir,
  }, async () => { await use(fixture.textModelID) })
}
