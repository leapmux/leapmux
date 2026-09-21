import type { MessageCategory } from '../../messageClassifier'
import type { MessageContentRenderContext } from '../../messageContentRenderer'
import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { dismissActiveTooltip, SHOW_DELAY_MS } from '~/components/common/Tooltip'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderMessageContent } from '../../messageContentRenderer'
import './plugin'

const tokenizeAsyncCalls = vi.hoisted(() => vi.fn())
const tokenizeAsyncMock = vi.hoisted(() => vi.fn(async (lang: string, code: string) => {
  tokenizeAsyncCalls(lang, code)
  return code.split('\n').map(line => [{ content: line, className: 'sk-codex-command-test' }])
}))

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: tokenizeAsyncMock,
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
  makeKey: (lang: string, code: string) => `${lang}\0${code}`,
}))

vi.mock('~/context/PreferencesContext', () => ({
  usePreferences: () => ({
    diffView: () => 'unified',
    expandAgentThoughts: () => true,
  }),
}))

function renderCodexItem(item: Record<string, unknown>, context?: MessageContentRenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const category: MessageCategory = { kind: 'tool_use' }
  return render(() => <>{renderMessageContent(parsed, context, category, AgentProvider.CODEX)}</>)
}

describe('codex command actions', () => {
  it('bounds collapsed action rendering and builds rich tooltips only on demand', async () => {
    tokenizeAsyncCalls.mockClear()
    const commandActions = Array.from({ length: 20 }, (_, index) => ({
      type: 'read',
      command: `sed -n '${index + 1}p' src/file.ts`,
      name: 'file.ts',
      path: '/repo/src/file.ts',
    }))
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'compound command',
      status: 'inProgress',
      commandActions,
    }, { workingDir: '/repo' })

    expect(container.querySelectorAll('[data-command-action]').length).toBeLessThanOrEqual(3)
    expect(container).toHaveTextContent('18 actions omitted')
    expect(tokenizeAsyncCalls).not.toHaveBeenCalled()
    expect(container.querySelector('[aria-label="Show all actions"]')).not.toBeNull()

    const firstAction = container.querySelector('[data-command-action="read"]')
    fireEvent.mouseEnter(firstAction!)
    await new Promise(resolve => setTimeout(resolve, SHOW_DELAY_MS + 10))
    await waitFor(() => expect(tokenizeAsyncCalls).toHaveBeenCalledTimes(1))
    dismissActiveTooltip()
  })

  it('renders structured actions and the process identifier through the shared execute renderer', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: '/bin/zsh -lc "sed and rg"',
      cwd: '/repo',
      processId: '79860',
      status: 'inProgress',
      commandActions: [
        { type: 'read', command: 'sed -n \'1,5p\' src/main.ts', name: 'main.ts', path: '/repo/src/main.ts' },
        { type: 'search', command: 'rg -n \'needle\' src', query: 'needle', path: 'src' },
      ],
    }, { workingDir: '/repo' })

    expect(container).toHaveTextContent('Read src/main.ts')
    expect(container).toHaveTextContent('Search for "needle" in src')
    expect(container).toHaveTextContent('Process ID:')
    expect(container).toHaveTextContent('79860')
  })

  it('syntax-highlights a raw command that an unknown action draws', async () => {
    tokenizeAsyncCalls.mockClear()
    renderCodexItem({
      type: 'commandExecution',
      command: 'compound command',
      status: 'inProgress',
      commandActions: [{ type: 'unknown', command: 'printf visible-action' }],
    })

    await waitFor(() => expect(tokenizeAsyncCalls).toHaveBeenCalledWith('bash', 'printf visible-action'))
  })

  it('puts one known action in the title with a highlighted command tooltip', async () => {
    tokenizeAsyncCalls.mockClear()
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'compound command',
      status: 'inProgress',
      commandActions: [{ type: 'read', command: 'sed -n \'1,5p\' src/main.ts', name: 'main.ts', path: '/repo/src/main.ts' }],
    }, { workingDir: '/repo' })
    const title = container.querySelector('[data-testid="execute-title"]')

    expect(title).toHaveTextContent('Read src/main.ts')
    expect(container.querySelector('[data-command-action]')).toBeNull()
    fireEvent.mouseEnter(title!)
    await new Promise(resolve => setTimeout(resolve, SHOW_DELAY_MS + 10))
    await waitFor(() => expect(tokenizeAsyncCalls).toHaveBeenCalledWith('bash', 'sed -n \'1,5p\' src/main.ts'))
    dismissActiveTooltip()
  })
})
