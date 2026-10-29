import type { PromptSuggestionChipProps } from './PromptSuggestionChip'
import type { PromptSuggestion } from '~/stores/chatPromptSuggestion'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getEditorRef, insertIntoAgentEditor } from '~/stores/editorRef.store'
import { PromptSuggestionChip } from './PromptSuggestionChip'

vi.mock('~/stores/editorRef.store', () => ({
  getEditorRef: vi.fn(),
  insertIntoAgentEditor: vi.fn(),
}))

beforeEach(() => {
  vi.mocked(insertIntoAgentEditor).mockReset().mockReturnValue('inserted')
  vi.mocked(getEditorRef).mockReset().mockReturnValue({
    get: () => '',
    set: vi.fn(),
    insert: vi.fn(),
    focus: vi.fn(),
    writable: () => true,
  })
})

describe('PromptSuggestionChip', () => {
  it.each([
    [
      'the agent works',
      { idle: false },
    ],
    [
      'the editor holds text',
      { editorEmpty: false },
    ],
    [
      'the suggestion is absent',
      { suggestion: undefined },
    ],
    [
      'the suggestion is blank',
      {
        suggestion: { text: '  ' },
      },
    ],
    [
      'the agent is absent',
      { agentId: '' },
    ],
  ] satisfies [string, Partial<PromptSuggestionChipProps>][])('hides when %s', (_name, override) => {
    const props: PromptSuggestionChipProps = {
      agentId: 'a',
      suggestion: { text: 'run the tests' },
      idle: true,
      editorEmpty: true,
      onDismiss: vi.fn(),
      ...override,
    }
    render(() => <PromptSuggestionChip {...props} />)
    expect(screen.queryByTestId('prompt-suggestion-chip')).not.toBeInTheDocument()
  })

  it('inserts the suggestion without a send action', async () => {
    const [suggestion, setSuggestion] = createSignal<PromptSuggestion | undefined>({ text: 'run the tests' })
    render(() => <PromptSuggestionChip agentId="a" suggestion={suggestion()} idle editorEmpty onDismiss={() => setSuggestion(undefined)} />)
    await fireEvent.click(screen.getByRole('button', { name: 'Use' }))
    expect(insertIntoAgentEditor).toHaveBeenCalledExactlyOnceWith('a', 'run the tests')
    expect(screen.queryByTestId('prompt-suggestion-chip')).not.toBeInTheDocument()
  })

  it('dismisses without changing the editor', async () => {
    const [suggestion, setSuggestion] = createSignal<PromptSuggestion | undefined>({ text: 'run the tests' })
    render(() => <PromptSuggestionChip agentId="a" suggestion={suggestion()} idle editorEmpty onDismiss={() => setSuggestion(undefined)} />)
    await fireEvent.click(screen.getByRole('button', { name: 'Dismiss suggestion' }))
    expect(insertIntoAgentEditor).not.toHaveBeenCalled()
    expect(screen.queryByTestId('prompt-suggestion-chip')).not.toBeInTheDocument()
  })

  it('keeps the suggestion when the editor refuses insertion', async () => {
    vi.mocked(insertIntoAgentEditor).mockReturnValue('refused')
    const dismiss = vi.fn()
    render(() => <PromptSuggestionChip agentId="a" suggestion={{ text: 'run the tests' }} idle editorEmpty onDismiss={dismiss} />)
    await fireEvent.click(screen.getByRole('button', { name: 'Use' }))
    expect(dismiss).not.toHaveBeenCalled()
    expect(screen.getByTestId('prompt-suggestion-chip')).toBeInTheDocument()
  })

  it('does not append to text typed before the empty-state signal updates', async () => {
    const editor = getEditorRef('a')!
    vi.mocked(getEditorRef).mockReturnValue({
      ...editor,
      get: () => 'typed now',
    })
    const dismiss = vi.fn()
    render(() => <PromptSuggestionChip agentId="a" suggestion={{ text: 'run the tests' }} idle editorEmpty onDismiss={dismiss} />)
    await fireEvent.click(screen.getByRole('button', { name: 'Use' }))
    expect(insertIntoAgentEditor).not.toHaveBeenCalled()
    expect(dismiss).not.toHaveBeenCalled()
  })

  it('does not queue text before the editor exists', async () => {
    vi.mocked(getEditorRef).mockReturnValue(undefined)
    const dismiss = vi.fn()
    render(() => <PromptSuggestionChip agentId="a" suggestion={{ text: 'run the tests' }} idle editorEmpty onDismiss={dismiss} />)
    await fireEvent.click(screen.getByRole('button', { name: 'Use' }))
    expect(insertIntoAgentEditor).not.toHaveBeenCalled()
    expect(dismiss).not.toHaveBeenCalled()
  })
})
