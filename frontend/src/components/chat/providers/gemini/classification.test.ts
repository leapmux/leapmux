import type { ProviderTranscriptCapability } from '../capabilities'
import { describe, expect, it, vi } from 'vitest'
import { GEMINI_MODE } from '~/generated/contracts/gemini-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createACPProvider } from '../acp/registerACPProvider'
import { input } from '../testUtils'
import { createGeminiClassifier } from './classification'
import './plugin'

describe('createGeminiClassifier', () => {
  const base = createACPProvider({ defaultPermissionMode: GEMINI_MODE.Default }).transcript
  const classify = createGeminiClassifier(base)

  it.each([
    [{ id: 'user-1', type: 'user', content: [{ text: 'The native child prompt.' }] }, 'user_text'],
    [{ id: 'model-1', type: 'gemini', content: 'The native answer.' }, 'assistant_text'],
    [{ id: 'model-1', type: 'gemini', content: '' }, 'hidden'],
    [{ id: 'model-1', type: 'gemini', content: [{ functionResponse: {} }] }, 'hidden'],
    [{ id: 'run_shell_command__native-tool', name: 'run_shell_command', status: 'success', args: { command: 'printf native' }, result: [{ functionResponse: { response: { output: 'native' } } }] }, 'tool_use'],
    [{ sessionUpdate: 'tool_call', toolCallId: 'read_file__native-tool', status: 'in_progress', kind: 'read', locations: [{ path: '/native/file.txt' }] }, 'tool_use'],
  ] as const)('retains the native or ACP category %s', (frame, category) => {
    expect(classify(input(frame, null, AgentProvider.GEMINI_CLI)).kind).toBe(category)
  })

  it('reads the selected native thought independently from visible model text', () => {
    const parsed = input({ id: 'model-1', type: 'gemini', content: 'Answer', thoughts: [{ subject: 'Inspect', description: 'Read the source.' }] }, null, AgentProvider.GEMINI_CLI)
    parsed.supplementalContent = { geminiMessagePart: 'thought', geminiMessagePartIndex: 0 }
    expect(classify(parsed)).toEqual({ kind: 'assistant_thinking' })
  })

  it('delegates with the exact captured receiver and input', () => {
    const original = base.classify
    const captured: Pick<ProviderTranscriptCapability, 'classify'> = {
      classify: vi.fn(function (this: Pick<ProviderTranscriptCapability, 'classify'>, ...args: Parameters<ProviderTranscriptCapability['classify']>) {
        expect(this).toBe(captured)
        return original(...args)
      }),
    }
    const parsed = input({ sessionUpdate: 'tool_call', toolCallId: 'read_file__native-tool', status: 'in_progress', kind: 'read', locations: [{ path: '/native/file.txt' }] }, null, AgentProvider.GEMINI_CLI)
    expect(createGeminiClassifier(captured)(parsed)).toEqual({ kind: 'tool_use' })
    expect(captured.classify).toHaveBeenCalledExactlyOnceWith(parsed, undefined)
  })
})
