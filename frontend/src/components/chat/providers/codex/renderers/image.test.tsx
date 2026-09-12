import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { providerFor } from '../../registry'
import { input } from '../../testUtils'
import { CodexImageViewRenderer } from './image'
import '../plugin'
import '../../testMocks'

const item = { type: 'imageView', id: 'call', path: '/image.png' }
const image = { data: 'iVBORw0KGgo=', mimeType: 'image/png' }

describe('codex image view roles', () => {
  it.each([0, 1789117866873])('uses notification timestamps to identify roles (%s)', (timestamp) => {
    const plugin = providerFor(AgentProvider.CODEX)!
    expect(plugin.spanRole?.(input({ item, startedAtMs: timestamp }))).toBe('opener')
    expect(plugin.spanRole?.(input({ item, completedAtMs: timestamp }))).toBe('result')
  })

  it('does not display an image from an opener before the result loads', () => {
    const { container } = render(() => <CodexImageViewRenderer parsed={{ item, startedAtMs: 1 }} context={{ premeasureMode: true, sources: testMessageSources({ role: () => 'opener', cachedFileImage: () => image }) }} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toContain('/image.png')
  })
})
