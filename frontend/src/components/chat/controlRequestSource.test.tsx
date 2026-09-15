import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { useControlRequestSource } from './controlRequestSource'

const sourceMessage = (seq: bigint, marker: string, provider = AgentProvider.PI) => makeMessage({ id: `source-${seq}-${marker}`, seq, agentProvider: provider, content: rawContent({ marker }) })
const request = (sourceSeq?: bigint, claimToken = 'first'): ControlRequest => ({ agentId: 'agent', requestId: 'request', claimToken, payload: {}, sourceSeq })

afterEach(() => {
  vi.useRealTimers()
})

describe('control source resolution', () => {
  it.each(['loaded', 'fetched'])('rejects a %s source from another provider session', async (location) => {
    const foreign = makeMessage({ ...sourceMessage(7n, 'foreign'), agentSessionId: 'foreign-session' })
    const fetchMessage = vi.fn(async () => foreign)
    const context = testMessageContext({ messages: () => location === 'loaded' ? [foreign] : [], fetchMessage })
    const View = () => {
      const source = useControlRequestSource(() => ({ ...request(7n), agentSessionId: 'current-session' }), () => context, () => AgentProvider.PI)
      return <div>{source() ? 'Source' : 'No source'}</div>
    }
    const { container } = render(() => <View />)
    if (location === 'fetched') {
      await waitFor(() => expect(fetchMessage).toHaveBeenCalledOnce())
      await fetchMessage.mock.results[0].value
    }
    expect(container.textContent).toBe('No source')
  })

  it.each(['outside history', 'evicted from history'])('applies delayed enrichment to a control source %s', async (location) => {
    const original = sourceMessage(7n, 'initial')
    const [messages, setMessages] = createSignal(location === 'outside history' ? [] : [original])
    let emit!: (message: AgentChatMessage) => void
    const fetchMessage = vi.fn(async () => original)
    const context = testMessageContext({
      messages,
      fetchMessage,
      subscribe: (observer) => {
        emit = observer
        return () => undefined
      },
    })
    const View = () => {
      const source = useControlRequestSource(() => request(7n), () => context, () => AgentProvider.PI)
      const marker = () => {
        const metadata = source()?.messageMetadata
        return isObject(metadata) ? pickString(metadata, 'marker') : pickString(source()?.parentObject, 'marker')
      }
      return <div>{marker()}</div>
    }
    const { container } = render(() => <View />)
    await waitFor(() => expect(container.textContent).toBe('initial'))
    setMessages([])
    emit(makeMessage({ ...original, supplementalRevision: 2n, supplementalContent: rawContent({ metadata: { marker: 'recovered' } }) }))
    await waitFor(() => expect(container.textContent).toBe('recovered'))
    emit(makeMessage({ ...original, supplementalRevision: 1n, supplementalContent: rawContent({ metadata: { marker: 'stale' } }) }))
    expect(container.textContent).toBe('recovered')
    expect(fetchMessage).toHaveBeenCalledTimes(location === 'outside history' ? 1 : 0)
  })

  it('retains the latest loaded source when the transcript window moves', () => {
    const [messages, setMessages] = createSignal([sourceMessage(7n, 'initial')])
    const fetchMessage = vi.fn(async () => undefined)
    const context = testMessageContext({ messages, fetchMessage })
    const View = () => {
      const source = useControlRequestSource(() => request(7n), () => context, () => AgentProvider.PI)
      return <div>{pickString(source()?.parentObject, 'marker')}</div>
    }
    const { container } = render(() => <View />)
    expect(container.textContent).toBe('initial')
    setMessages([sourceMessage(7n, 'updated')])
    expect(container.textContent).toBe('updated')
    setMessages([])
    expect(container.textContent).toBe('updated')
    expect(fetchMessage).not.toHaveBeenCalled()
  })

  it('does not apply a previous request instance after its source arrives late', async () => {
    let resolve!: (value: AgentChatMessage) => void
    const fetchMessage = vi.fn(() => new Promise<AgentChatMessage>((complete) => {
      resolve = complete
    }))
    const context = testMessageContext({ fetchMessage })
    const [current, setCurrent] = createSignal(request(7n))
    const View = () => {
      const source = useControlRequestSource(current, () => context, () => AgentProvider.PI)
      return <div>{pickString(source()?.parentObject, 'marker') || 'No source'}</div>
    }
    const { container } = render(() => <View />)
    await waitFor(() => expect(fetchMessage).toHaveBeenCalledOnce())
    setCurrent(request(undefined, 'second'))
    resolve(sourceMessage(7n, 'stale'))
    await fetchMessage.mock.results[0].value
    expect(container.textContent).toBe('No source')
  })

  it.each([undefined, 0n, -1n])('does not fetch without a positive source sequence (%s)', (sequence) => {
    const fetchMessage = vi.fn(async () => undefined)
    const context = testMessageContext({ fetchMessage })
    const View = () => {
      const source = useControlRequestSource(() => request(sequence), () => context, () => undefined)
      return <div>{source() ? 'Source' : 'No source'}</div>
    }
    const { container } = render(() => <View />)
    expect(container.textContent).toBe('No source')
    expect(fetchMessage).not.toHaveBeenCalled()
  })

  it('retries a failed load, and stops at the attempt limit', async () => {
    vi.useFakeTimers()
    const fetchMessage = vi.fn(async (): Promise<AgentChatMessage> => {
      throw new Error('Connection lost')
    })
    const context = testMessageContext({ fetchMessage })
    const View = () => {
      const source = useControlRequestSource(() => request(7n), () => context, () => AgentProvider.PI)
      return <div>{source() ? 'Source' : 'No source'}</div>
    }
    render(() => <View />)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetchMessage).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(400)
    expect(fetchMessage).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1600)
    expect(fetchMessage).toHaveBeenCalledTimes(3)
    // The budget is spent. No timer remains, so no further attempt runs.
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchMessage).toHaveBeenCalledTimes(3)
  })

  it('shows the source that a retry loads', async () => {
    vi.useFakeTimers()
    const fetchMessage = vi.fn<() => Promise<AgentChatMessage>>()
      .mockRejectedValueOnce(new Error('Connection lost'))
      .mockResolvedValueOnce(sourceMessage(7n, 'recovered'))
    const context = testMessageContext({ fetchMessage })
    const View = () => {
      const source = useControlRequestSource(() => request(7n), () => context, () => AgentProvider.PI)
      return <div>{pickString(source()?.parentObject, 'marker') || 'No source'}</div>
    }
    const { container } = render(() => <View />)
    await vi.advanceTimersByTimeAsync(1)
    expect(container.textContent).toBe('No source')
    await vi.advanceTimersByTimeAsync(400)
    expect(container.textContent).toBe('recovered')
  })

  it('does not retry a load that an abort stopped', async () => {
    vi.useFakeTimers()
    const fetchMessage = vi.fn(async (): Promise<AgentChatMessage> => {
      throw new DOMException('Stopped', 'AbortError')
    })
    const context = testMessageContext({ fetchMessage })
    const View = () => {
      const source = useControlRequestSource(() => request(7n), () => context, () => AgentProvider.PI)
      return <div>{source() ? 'Source' : 'No source'}</div>
    }
    render(() => <View />)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetchMessage).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchMessage).toHaveBeenCalledTimes(1)
  })

  it('gives each request its own retry budget', async () => {
    vi.useFakeTimers()
    const fetchMessage = vi.fn(async (): Promise<AgentChatMessage> => {
      throw new Error('Connection lost')
    })
    const context = testMessageContext({ fetchMessage })
    const [current, setCurrent] = createSignal(request(7n))
    const View = () => {
      const source = useControlRequestSource(current, () => context, () => AgentProvider.PI)
      return <div>{source() ? 'Source' : 'No source'}</div>
    }
    render(() => <View />)
    await vi.advanceTimersByTimeAsync(2100)
    expect(fetchMessage).toHaveBeenCalledTimes(3)
    // A second request instance is a new card, so it starts a full budget.
    setCurrent(request(7n, 'second'))
    await vi.advanceTimersByTimeAsync(2100)
    expect(fetchMessage).toHaveBeenCalledTimes(6)
  })

  it('rejects a loaded source from another provider', () => {
    const context = testMessageContext({ messages: () => [sourceMessage(7n, 'foreign', AgentProvider.ZCODE)] })
    const View = () => {
      const source = useControlRequestSource(() => request(7n), () => context, () => AgentProvider.PI)
      return <div>{source() ? 'Source' : 'No source'}</div>
    }
    const { container } = render(() => <View />)
    expect(container.textContent).toBe('No source')
  })
})
