import { describe, expect, it } from 'vitest'
import { QODER_FRAME_KIND, QODER_SYSTEM_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { qoderNeedsRequest, qoderSpanRole } from './spanRole'
import '~/components/chat/providers'

describe('qoder plugin', () => {
  it('identifies a stored compaction start only for the shared history filter', () => {
    const read = providerFor(AgentProvider.QODER)?.transcript.compactionStart
    expect(read?.({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: 'compacting' })).toBe(true)
    expect(read?.({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: null })).toBe(false)
    expect(read?.({ type: QODER_FRAME_KIND.Assistant, subtype: QODER_SYSTEM_SUBTYPE.Status, status: 'compacting' })).toBe(false)
  })

  it('needs the request only for a tool_result frame', () => {
    const needsRequest = providerFor(AgentProvider.QODER)?.transcript.needsRequest
    expect(needsRequest).toBe(qoderNeedsRequest)
    const frame = (type: string, block: Record<string, unknown>) => input({
      type,
      message: {
        content: [block],
      },
    }, null, AgentProvider.QODER)
    expect(qoderNeedsRequest(frame(QODER_FRAME_KIND.Assistant, {
      type: 'tool_use',
      id: 'call',
      name: 'Bash',
      input: {},
    }))).toBe(false)
    expect(qoderNeedsRequest(frame(QODER_FRAME_KIND.User, { type: 'tool_result', tool_use_id: 'call', content: 'out' }))).toBe(true)
    expect(qoderNeedsRequest(frame(QODER_FRAME_KIND.Assistant, { type: 'text', text: 'Native answer.' }))).toBe(false)
  })

  // A turn that ends while Qoder streams a call stores the `tool_use` frame with a
  // completion, and Qoder sends no result for it: that frame is the final row of its
  // span. It states its own arguments.
  it('places a stopped tool_use frame on the result side', () => {
    const stopped = {
      ...input({
        type: QODER_FRAME_KIND.Assistant,
        message: {
          content: [
            {
              type: 'tool_use',
              id: 'call',
              name: 'Bash',
              input: { command: 'ls' },
            },
          ],
        },
      }, null, AgentProvider.QODER),
      completion: MessageCompletion.INTERRUPTED,
    }
    expect(qoderSpanRole(stopped)).toBe('result')
    expect(qoderNeedsRequest(stopped)).toBe(false)
  })
})
