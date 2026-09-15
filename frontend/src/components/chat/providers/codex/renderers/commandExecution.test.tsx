import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../../../messageRenderers'
import { providerFor } from '../../registry'
import { input } from '../../testUtils'
import '../plugin'
import '../../testMocks'

// A commandExecution whose opener is not beside it.
//
// A stop retains the opener's last frame as the call's result and closes the span. When
// the command later ends on its own, its completion arrives with no request to pair
// with -- seq 213 of the census database, RL-047. The row shows what the command
// printed and invents no name.
describe('codex command execution without its opener', () => {
  it('shows an unmatched completion by its content alone', () => {
    const parsed = { item: { id: 'exec-orphan', type: 'commandExecution', status: 'completed', command: '/bin/zsh -lc \'sleep 45; echo marker-4471\'', aggregatedOutput: 'marker-4471\n', exitCode: 0 } }
    const plugin = providerFor(AgentProvider.CODEX)!
    const { container } = render(() => renderMessageContent(parsed, {
      premeasureMode: true,
      sources: testMessageSources({ current: () => input(parsed), request: () => undefined }),
    }, plugin.classify(input(parsed)), AgentProvider.CODEX))
    expect(container.textContent).toContain('marker-4471')
    for (const invented of ['Read file', 'Search', 'Fetch'])
      expect(container.textContent).not.toContain(invented)
  })
})
