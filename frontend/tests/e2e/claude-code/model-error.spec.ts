import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest } from '../claude-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

claudeTest('reports a native model error and accepts the next turn', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
  void authenticatedClaudeWorkspace
  // In auto mode, Claude Code 2.1.289 sends the dangerous-tool-use beta with a
  // `safeguards` field. It answers an HTTP 400 that it does not recognise with
  // ONE retry without them ("retry:safeguards-unclaimed"), so the failed turn
  // sends two requests, and both must fail.
  const [first, retry] = await exerciseModelError({ page, modelScript, provider: AgentProvider.CLAUDE_CODE }, { attempts: 2 })
  expect(isObject(first?.body) && Object.hasOwn(first.body, 'safeguards')).toBe(true)
  expect(isObject(retry?.body) && !Object.hasOwn(retry.body, 'safeguards')).toBe(true)
})
