/**
 * Qwen Code e2e test fixtures.
 */
import type { AgentWorkspace } from './helpers/workspace'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { writePrivateJSON } from './helpers/privateConfigFile'
import { gitRepositoryWorkingDir } from './helpers/providerWorkingDir'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { createProviderTest } from './provider-fixture-factory'
import { nativeContext, QWEN_AGENT } from './qwen-code/scenarios'

export const QWEN_E2E_SKIP_REASON: string | null = missingBinaryReason('qwen', 'Qwen Code E2E requires a qwen CLI on PATH')

export const qwenTest = createProviderTest({
  agent: QWEN_AGENT,
  nativeContext,
  skipReason: QWEN_E2E_SKIP_REASON,
  workspaceName: 'authenticatedQwenWorkspace',
}).extend<{
  /**
   * A workspace whose agent offers follow-up suggestions. The shared Qwen
   * settings turn them off, because the suggestion is a model call that no
   * test scripts; the project settings of this repository turn them back on
   * for the spec that scripts that call. The repository keeps the rule of
   * {@link QWEN_AGENT}, so the agent still reads no file above it.
   */
  suggestingQwenWorkspace: AgentWorkspace
}>({
  suggestingQwenWorkspace: authenticatedAgentWorkspace({
    ...QWEN_AGENT,
    prefix: 'qwen-e2e-suggest',
    workingDir: (prefix) => {
      const repository = gitRepositoryWorkingDir(prefix)
      const qwenDir = join(repository, '.qwen')
      mkdirSync(qwenDir, { recursive: true })
      writePrivateJSON(join(qwenDir, 'settings.json'), {
        ui: { enableFollowupSuggestions: true },
      })
      return repository
    },
  }),
})
