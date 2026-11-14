import type { DotCluster } from './chatRailPolicy'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { MarkType } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { DotPreviewCard } from './ChatScrollRailPreview'

// The card's pointer, wheel and selection behaviour is driven end-to-end through the real rail in
// ChatScrollRail.test.tsx, because all of it is a conversation between the card and the rail's dot
// handlers. What is left here is how the card renders the dot that it is handed, which is easier
// to observe with the props in hand than through the component that owns them.

function dot(seq: bigint, count = 1): DotCluster {
  return {
    seq,
    topPx: 100,
    type: MarkType.USER_MESSAGE,
    count,
  }
}

function cardProps(overrides: Partial<Parameters<typeof DotPreviewCard>[0]> = {}) {
  return {
    dot: dot(2n),
    previewFor: () => 'a preview',
    onHoldStart: vi.fn(),
    onHoldEnd: vi.fn(),
    onPressChange: vi.fn(),
    ...overrides,
  }
}

describe('DotPreviewCard rendering', () => {
  it('renders the preview for the dot it was handed, resolved by that dot\'s seq', async () => {
    // The card takes `(seq) => text` and the whole cluster, rather than a thunk closed over one
    // dot: it must ask for ITS dot's preview, not for whichever dot the closure captured.
    const previewFor = vi.fn((seq: bigint) => (seq === 7n
      ? 'the seventh message'
      : 'the wrong one'))
    const { getByTestId } = render(() => (
      <DotPreviewCard {...cardProps({
        dot: dot(7n),
        previewFor,
      })}
      />
    ))
    await settleWorkerReplies()
    expect(previewFor).toHaveBeenCalledWith(7n)
    expect(getByTestId('chat-scroll-rail-preview')).toHaveTextContent('the seventh message')
  })

  it('shows a loading line while the preview is still resolving, and the mark label when it resolves empty', () => {
    // Three states, and `''` is the one a falsy check gets wrong: it means "resolved, and this
    // mark has no previewable text", which must show the label rather than the loading line.
    const { getByTestId, unmount } = render(() => (
      <DotPreviewCard {...cardProps({
        previewFor: () => undefined,
      })}
      />
    ))
    expect(getByTestId('chat-scroll-rail-preview')).toHaveTextContent('Loading preview')
    unmount()

    const resolved = render(() => (
      <DotPreviewCard {...cardProps({
        previewFor: () => '',
      })}
      />
    ))
    expect(resolved.getByTestId('chat-scroll-rail-preview')).toHaveTextContent('Your message')
    expect(resolved.getByTestId('chat-scroll-rail-preview')).not.toHaveTextContent('Loading preview')
  })

  it('heads a CLUSTER card with its member count, and a single-mark card with none', () => {
    const cluster = render(() => (
      <DotPreviewCard {...cardProps({
        dot: dot(2n, 4),
      })}
      />
    ))
    expect(cluster.getByTestId('chat-scroll-rail-preview')).toHaveTextContent('4 messages')
    cluster.unmount()

    const single = render(() => (
      <DotPreviewCard {...cardProps({
        dot: dot(2n, 1),
      })}
      />
    ))
    expect(single.getByTestId('chat-scroll-rail-preview')).not.toHaveTextContent('messages')
  })
})
