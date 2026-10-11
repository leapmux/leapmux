import { claudeTest } from '../claude-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'

/**
 * Claude's live counter is the thinking-token estimate of its `system`/`thinking_tokens` frames.
 * Claude Code 2.1.289 derives those frames from thinking deltas only, so a text-only answer carries no counter.
 * The scripted answer therefore thinks first. The two stream gates hold inside the thinking block.
 */
const THINKING = 'NATIVEPROGRESSTHINKING weighs the scripted answer. '.repeat(6)

claudeTest('advances the native model counter while each output segment remains held', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true, step: { reasoning: THINKING } })
})
