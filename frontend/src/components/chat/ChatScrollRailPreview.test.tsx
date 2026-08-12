import type { DotCluster } from './chatRailPolicy'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { MarkType } from '~/generated/leapmux/v1/agent_pb'
import { DotPreviewCard } from './ChatScrollRailPreview'

// The card's pointer, wheel and selection behaviour is driven end-to-end through the real rail in
// ChatScrollRail.test.tsx, because all of it is a conversation between the card and the rail's dot
// handlers. What is left here is the card's own reporting contract, which is easier to observe
// with the callbacks in hand than through the component that owns them.

function dot(seq: bigint, count = 1): DotCluster {
  return { seq, topPx: 100, type: MarkType.USER_MESSAGE, count }
}

function cardProps(overrides: Partial<Parameters<typeof DotPreviewCard>[0]> = {}) {
  return {
    topPx: 100,
    dot: dot(2n),
    previewFor: () => 'a preview',
    onHoldStart: vi.fn(),
    onHoldEnd: vi.fn(),
    onPressChange: vi.fn(),
    onHeightChange: vi.fn(),
    ...overrides,
  }
}

describe('dotPreviewCard height reporting', () => {
  it('reports its height as soon as it mounts, before any resize', () => {
    // The rail clamps the card's Y against this. Without the report the clamp has nothing to work
    // with for the card's whole life, because jsdom fires no resize and a card that never changes
    // size never would either -- so the mount-time report is not an optimisation, it is the only
    // measurement a short-lived card ever makes.
    const onHeightChange = vi.fn()
    render(() => <DotPreviewCard {...cardProps({ onHeightChange })} />)
    expect(onHeightChange).toHaveBeenCalledTimes(1)
    // jsdom does no layout, so the value is 0 here; that it ARRIVES is what this pins. The clamp
    // arithmetic itself is unit-tested against injected heights in chatDotPreview.test.ts.
    expect(onHeightChange).toHaveBeenCalledWith(0)
  })

  it('retracts its height when it unmounts, so the next card is not clamped against it', () => {
    const onHeightChange = vi.fn()
    const { unmount } = render(() => <DotPreviewCard {...cardProps({ onHeightChange })} />)
    onHeightChange.mockClear()

    unmount()

    // A height left behind belongs to a card that no longer exists. The next card would be
    // clamped against it on its very first frame -- placed for a tall card while it renders a
    // one-line preview, or the reverse.
    expect(onHeightChange).toHaveBeenCalledWith(0)
  })
})

describe('dotPreviewCard rendering', () => {
  it('renders the preview for the dot it was handed, resolved by that dot\'s seq', () => {
    // The card takes `(seq) => text` and the whole cluster, rather than a thunk closed over one
    // dot: it must ask for ITS dot's preview, not for whichever dot the closure captured.
    const previewFor = vi.fn((seq: bigint) => (seq === 7n ? 'the seventh message' : 'the wrong one'))
    const { getByTestId } = render(() => <DotPreviewCard {...cardProps({ dot: dot(7n), previewFor })} />)
    expect(previewFor).toHaveBeenCalledWith(7n)
    expect(getByTestId('chat-scroll-rail-preview')).toHaveTextContent('the seventh message')
  })

  it('shows a loading line while the preview is still resolving, and the mark label when it resolves empty', () => {
    // Three states, and `''` is the one a falsy check gets wrong: it means "resolved, and this
    // mark has no previewable text", which must show the label rather than the loading line.
    const { getByTestId, unmount } = render(() => (
      <DotPreviewCard {...cardProps({ previewFor: () => undefined })} />
    ))
    expect(getByTestId('chat-scroll-rail-preview')).toHaveTextContent('Loading preview')
    unmount()

    const resolved = render(() => <DotPreviewCard {...cardProps({ previewFor: () => '' })} />)
    expect(resolved.getByTestId('chat-scroll-rail-preview')).toHaveTextContent('Your message')
    expect(resolved.getByTestId('chat-scroll-rail-preview')).not.toHaveTextContent('Loading preview')
  })

  it('heads a CLUSTER card with its member count, and a single-mark card with none', () => {
    const cluster = render(() => <DotPreviewCard {...cardProps({ dot: dot(2n, 4) })} />)
    expect(cluster.getByTestId('chat-scroll-rail-preview')).toHaveTextContent('4 messages')
    cluster.unmount()

    const single = render(() => <DotPreviewCard {...cardProps({ dot: dot(2n, 1) })} />)
    expect(single.getByTestId('chat-scroll-rail-preview')).not.toHaveTextContent('messages')
  })
})
