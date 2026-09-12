import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../../messageRenderers'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'
import '../testMocks'

// Claude's tool result is a `user` row carrying a `tool_result` block, and its tool
// NAME lives on the `tool_use` row it answers. With no request beside it, the row has
// only its content to show, and it must show that rather than a name it never read.
describe('claude tool rendering', () => {
  it('shows an unmatched completion by its content alone', () => {
    const parsed = { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_orphan', content: 'recovered output' }] } }
    const plugin = providerFor(AgentProvider.CLAUDE_CODE)!
    const { container } = render(() => renderMessageContent(parsed, {
      premeasureMode: true,
      sources: testMessageSources({ current: () => input(parsed), request: () => undefined }),
    }, plugin.classify(input(parsed)), AgentProvider.CLAUDE_CODE))
    expect(container.textContent).toContain('recovered output')
    for (const invented of ['Run command', 'Read file', 'Search'])
      expect(container.textContent).not.toContain(invented)
  })
})
