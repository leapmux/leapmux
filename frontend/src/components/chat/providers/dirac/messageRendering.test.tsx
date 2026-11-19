import { render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentProvider, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { INTERRUPTION_MARKER } from '../../assembledMessage'
import { MessageBubble } from '../../MessageBubble'
import './plugin'

describe('MessageBubble', () => {
  it('updates a native partial answer when its stored completion arrives', async () => {
    const record = {
      type: 'leapmux.dirac.message',
      session_id: 'child',
      parent_session_id: 'parent',
      tasklet_id: 'Witten',
      message: {
        id: 'native-partial',
        conversationHistoryIndex: 0,
        content: { type: 'markdown', role: 'assistant', content: 'Native partial answer' },
      },
    }
    const initial = makeMessage({
      agentProvider: AgentProvider.DIRAC,
      agentSessionId: 'child',
      source: MessageSource.AGENT,
      content: rawContent(record),
    })
    const [message, setMessage] = createSignal(initial)
    render(() => <PreferencesProvider><MessageBubble message={message()} /></PreferencesProvider>)
    await settleWorkerReplies()
    expect(screen.getByTestId('message-content')).toHaveTextContent('Native partial answer')
    expect(screen.queryByRole('note')).toBeNull()
    setMessage({ ...initial, completion: MessageCompletion.INTERRUPTED, supplementalRevision: 1n })
    await settleWorkerReplies()
    expect(screen.getByRole('note')).toHaveTextContent(INTERRUPTION_MARKER)
    expect(screen.getByTestId('message-content')).toHaveTextContent('Native partial answer')
  })
})
