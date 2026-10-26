import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { showWarnToast } from '~/components/common/Toast'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { forgetAgentAttachments } from './attachments'
import { useChatAttachments } from './useChatAttachments'

vi.mock('./providers/registry', () => ({
  providerFor: () => ({ configuration: { attachments: { text: true, image: true, pdf: false, audio: ['audio/wav'], binary: false } } }),
}))
vi.mock('~/components/common/Toast', () => ({ showWarnToast: vi.fn() }))

describe('useChatAttachments', () => {
  it('accepts inferred audio and rejects unrelated binary files in the same selection', async () => {
    vi.mocked(showWarnToast).mockClear()
    let dispose = () => {}
    const state = createRoot((cleanup) => {
      dispose = cleanup
      return useChatAttachments({ agentId: () => 'restricted-audio', agentProvider: () => AgentProvider.CODEX, providerLabel: () => 'Codex' })
    })
    try {
      const count = await state.addFiles([
        new File([new Uint8Array([0, 1, 2])], 'voice.wav'),
        new File([new Uint8Array([0, 255])], 'archive.bin', { type: 'application/octet-stream' }),
        new File([new Uint8Array([0, 255])], 'unsupported.flac', { type: 'audio/flac' }),
      ])
      expect(count).toBe(1)
      expect(state.attachments().map(file => file.filename)).toEqual(['voice.wav'])
      expect(state.attachments()[0]?.mimeType).toBe('audio/wav')
      expect(showWarnToast).toHaveBeenCalledWith('Codex does not support binary attachments')
      expect(showWarnToast).toHaveBeenCalledWith('Codex does not support this audio file type')
      expect(state.acceptAttribute()).toContain('audio/wav')
    }
    finally {
      dispose()
      forgetAgentAttachments('restricted-audio')
    }
  })
})
