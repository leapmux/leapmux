import { requireLoopbackHttpURL } from '../helpers/agentEnvironmentInputs'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'

/** Add a Google route to the private native configuration without rewriting its other providers. */
export function fastagentAudioConfiguration(original: string, openAIBaseURL: string): string {
  if (!original.trim() || /^google\s*:/m.test(original))
    throw new Error('The Fast Agent audio fixture requires an existing configuration without a Google provider.')
  const endpoint = requireLoopbackHttpURL(openAIBaseURL, 'The Fast Agent audio fixture endpoint')
  if (endpoint.pathname !== '/v1')
    throw new Error('The Fast Agent audio fixture requires the private OpenAI /v1 endpoint.')
  return `${original}\ngoogle:\n  api_key: ${JSON.stringify(MODEL_KEY)}\n  base_url: ${JSON.stringify(endpoint.origin)}\n`
}
