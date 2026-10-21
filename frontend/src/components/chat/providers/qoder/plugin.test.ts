import { describe, expect, it } from 'vitest'
import { QODER_FRAME_KIND, QODER_SYSTEM_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import '~/components/chat/providers'

describe('qoder plugin', () => {
  it('identifies a stored compaction start only for the shared history filter', () => {
    const read = providerFor(AgentProvider.QODER)?.transcript.compactionStart
    expect(read?.({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: 'compacting' })).toBe(true)
    expect(read?.({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: null })).toBe(false)
    expect(read?.({ type: QODER_FRAME_KIND.Assistant, subtype: QODER_SYSTEM_SUBTYPE.Status, status: 'compacting' })).toBe(false)
  })
})
