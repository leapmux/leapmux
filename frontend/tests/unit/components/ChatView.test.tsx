import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { ChatView } from '~/components/chat/ChatView'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { ContentCompression } from '~/generated/leapmux/v1/agent_pb'

// jsdom does not provide ResizeObserver
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

function makeMessage(role: string, text: string, id: string = '1'): AgentChatMessage {
  const content = JSON.stringify({
    message: {
      content: [{ type: 'text', text }],
    },
  })
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id,
    role,
    content: new TextEncoder().encode(content),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    createdAt: '',
  }
}

describe('chatView', () => {
  it('renders empty state when no messages', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeTruthy()
  })

  it('renders messages', () => {
    const messages = [
      makeMessage('user', 'Hello', '1'),
      makeMessage('assistant', 'Hi there', '2'),
    ]
    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Hello')).toBeTruthy()
    expect(screen.getByText('Hi there')).toBeTruthy()
  })

  it('renders streaming text', async () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="Thinking..." />
      </PreferencesProvider>
    ))
    // Streaming text rendering is throttled via requestAnimationFrame
    await waitFor(() => expect(screen.getByText('Thinking...')).toBeTruthy())
  })

  it('renders chat container', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('chat-container')).toBeTruthy()
  })
})
