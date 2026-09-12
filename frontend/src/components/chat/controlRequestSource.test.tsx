import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickString } from '~/lib/jsonPick'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { useControlRequestSource } from './controlRequestSource'

const sourceMessage = (seq: bigint, marker: string, provider = AgentProvider.PI) => makeMessage({ id: `source-${seq}-${marker}`, seq, agentProvider: provider, content: rawContent({ marker }) })
const request = (sourceSeq?: bigint, claimToken = 'first'): ControlRequest => ({ agentId: 'agent', requestId: 'request', claimToken, payload: {}, sourceSeq })

describe('control source resolution', () => {
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
