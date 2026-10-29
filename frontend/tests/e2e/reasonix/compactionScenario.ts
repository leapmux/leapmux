import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { PrivateWorkerSetup } from '../helpers/privateNativeWorkspace'
import { createMockAgentEnvironment, MOCK_MODELS, MODEL_KEY, REASONIX_ALT_PROVIDER_ID, REASONIX_PLAIN_PROVIDER_ID } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent, nativeModelLastUserText } from '../helpers/nativeScenario'
import { createReasonixEnvironment } from '../helpers/reasonixEnvironment'
import { exerciseCompactAsModelText } from '../helpers/unsupportedCompaction'

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
  return {
    agentEnv: {
      ...environment.env,
      ...nativeEnv,
    },
    setup: undefined,
  }
}

/** Remove only the audited native host wrappers around one current user prompt. */
export function reasonixCompactionUserInput(request: MockModelRequestRecord, workingDir: string): string {
  const text = nativeModelLastUserText(request)
  const prefix = `<workspace>\nCurrent workspace: "${workingDir}". Shell commands start there and every tool resolves relative paths from it.\nVersion control: none (not a repository).\n</workspace>\n\n`
  const suffix = '\n\n<execution-policy preset="balanced" version="3">\nverify=targeted\n</execution-policy>'
  if (!workingDir || !text.startsWith(prefix) || !text.endsWith(suffix))
    throw new Error('The native Reasonix command input differs from its audited host wrappers.')
  return text.slice(prefix.length, -suffix.length)
}

/** Prove that the exact command remains ordinary input inside Reasonix's native host wrappers. */
export async function exerciseReasonixCompactAsModelText(context: ManagedNativeScenarioContext): Promise<void> {
  const { workingDir } = await currentNativeAgent(context)
  await exerciseCompactAsModelText(context, {
    lastUserText: request => reasonixCompactionUserInput(request, workingDir),
  })
}
