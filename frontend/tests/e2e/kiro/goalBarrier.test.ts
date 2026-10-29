import type { Page } from '@playwright/test'
import type { MockModelServer } from '../helpers/mockModelServer'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { withCleanup } from '../helpers/cleanup'
import { MOCK_MODEL_IDS } from '../helpers/mockAgentEnvironment'
import { createMockModelServer } from '../helpers/mockModelServer'
import { startModelScript } from '../helpers/modelScriptFixture'
import { sendKiroGoalBarrier } from './goalBarrier'

const calls = vi.hoisted(() => ({
  send: vi.fn(),
}))
vi.mock('../helpers/ui', () => ({
  sendMessage: calls.send,
  waitForAgentIdle: async () => {},
  assistantBubbles: () => ({
    filter: () => ({
      first: () => document.body,
    }),
  }),
}))
vi.mock('@playwright/test', async () => ({ expect: (await import('vitest')).expect }))

afterEach(() => vi.resetAllMocks())

async function withScript(use: (script: ModelScript, server: MockModelServer) => Promise<void>) {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  await withCleanup(async () => {
    const { script, finish } = await startModelScript(server.url)
    await withCleanup(() => use(script, server), () => finish(true))
  }, () => server.close())
}

describe('sendKiroGoalBarrier', () => {
  it('keeps a native workflow notification from consuming the user answer', () => withScript(async (script, server) => {
    const responses: string[] = []
    const parentSession = 'native-parent-session'
    await script.fallback({ text: 'The native notification completed.' })
    const reply = async (content: string) => {
      const response = await fetch(`${server.url}/`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-amz-json-1.0', 'x-amz-target': 'KiroRuntimeService.GenerateAssistantResponse' },
        body: JSON.stringify({
          conversationState: {
            conversationId: parentSession,
            history: [
              {
                userInputMessage: {
                  content: script.prompt('Reply once after the native goal is cleared.'),
                },
              },
            ],
            currentMessage: {
              userInputMessage: { content, origin: 'AI_EDITOR', modelId: 'kiro-e2e' },
            },
          },
          agentMode: 'vibe',
        }),
      })
      expect(response.status).toBe(200)
      responses.push(new TextDecoder().decode(await response.arrayBuffer()))
    }
    calls.send.mockImplementation(async (_page: Page, prompt: string) => {
      await reply(script.prompt('The user stopped the workflow "goal" on purpose.'))
      await reply(prompt)
    })
    const page = {} as Page
    await sendKiroGoalBarrier({ page, modelScript: script }, {
      name: 'cleared-user-turn',
      prompt: 'Reply once after the native goal is cleared.',
      answer: 'The requested user turn completed.',
    })
    expect(responses[0]).toContain('The native notification completed.')
    expect(responses[0]).not.toContain('The requested user turn completed.')
    expect(responses[1]).toContain('The requested user turn completed.')
    expect((await script.status()).unexpectedRequests).toEqual([])
  }))

  it.each(['name', 'prompt', 'answer'] as const)('refuses an empty %s before sending a message', field => withScript(async (script) => {
    const options = {
      name: 'barrier',
      prompt: 'Reply to the user.',
      answer: 'The user turn ended.',
      [field]: ' ',
    }
    await expect(sendKiroGoalBarrier({ page: {} as Page, modelScript: script }, options)).rejects.toThrow(`${field} must not be empty`)
    expect(calls.send).not.toHaveBeenCalled()
    expect((await script.status()).requests).toEqual([])
  }))
})
