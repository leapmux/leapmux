import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'

/**
 * Claude's live counter is the thinking-token estimate of its `system`/`thinking_tokens` frames.
 * Claude Code 2.1.289 derives those frames from thinking deltas only, so a text-only answer carries no counter.
 * The scripted answer therefore thinks first. The two stream gates hold inside the thinking block.
 */
const THINKING = 'NATIVEPROGRESSTHINKING weighs the scripted answer. '.repeat(6)

claudeTest('advances the native model counter while each output segment remains held', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseGenerationProgress({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }, {
    supported: true,
    counter: 'tokens',
    step: { reasoning: THINKING },
  })
})
