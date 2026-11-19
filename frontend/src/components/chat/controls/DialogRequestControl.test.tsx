import type { DialogPrompt } from '../model/controlPrompt'
import type { ControlDecisionSender } from './types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { showWarnToast } from '~/components/common/Toast'
import { DialogAction } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testControlRequest } from '~/test-support/controlRequests'
import { ControlRequestActions } from '../ControlRequestBanner'
import { DialogRequestActions, DialogRequestContent, dialogTimeoutHint } from './DialogRequestControl'
import { createControlAnswerState } from './types'

// The Enter key of an input sends outside the decision buttons, so its failure
// reaches the reader through the shared action toast. The mock records it.
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
}))

/**
 * A dialog that resolves itself is one the reader must be able to see a deadline on,
 * so the sentence has to state a deadline the reader can act on.
 */
describe('dialogTimeoutHint', () => {
  it('states a sub-second deadline in milliseconds', () => {
    // Rounding to whole seconds read "Auto-resolves in 0s if no response." for every
    // deadline under 500 ms, which tells the reader the dialog already expired.
    expect(dialogTimeoutHint({ title: 'Continue?', variant: 'confirm', timeoutMs: 400 }))
      .toBe('Auto-resolves in 400ms if no response.')
  })

  it('states a longer deadline in whole seconds', () => {
    expect(dialogTimeoutHint({ title: 'Continue?', variant: 'confirm', timeoutMs: 30_000 }))
      .toBe('Auto-resolves in 30s if no response.')
  })

  it('states nothing for a dialog with no deadline', () => {
    expect(dialogTimeoutHint({ title: 'Continue?', variant: 'confirm' })).toBeNull()
  })

  // Zero is a deadline that the model states. Only an absent one states nothing.
  it('states a zero deadline in milliseconds rather than nothing', () => {
    expect(dialogTimeoutHint({ title: 'Continue?', variant: 'confirm', timeoutMs: 0 }))
      .toBe('Auto-resolves in 0ms if no response.')
  })
})

const request: ControlRequest = testControlRequest({
  requestId: 'd1',
  agentId: 'agent-1',
  prompt: {
    kind: 'dialog',
    dialog: { title: 'Proceed?', variant: 'confirm' },
  },
})

/** The dialog decisions that each call of a sender carried, as an action and a value. */
function sent(onDecide: ReturnType<typeof vi.fn<ControlDecisionSender>>): Array<{ action: DialogAction, value: string }> {
  return onDecide.mock.calls.map(([decision]) => {
    if (decision.kind.case !== 'dialog')
      throw new Error(`the sender carried a ${decision.kind.case} decision`)
    return { action: decision.kind.value.action, value: decision.kind.value.value }
  })
}

/** The actions of one dialog, with a sender that records each decision. */
function renderActions(dialog: DialogPrompt, answerState = createControlAnswerState()) {
  const onDecide = vi.fn<ControlDecisionSender>(async () => {})
  const view = render(() => (
    <DialogRequestActions
      request={request}
      dialog={dialog}
      answerState={answerState}
      onDecide={onDecide}
      hasEditorContent={false}
      onTriggerSend={() => {}}
    />
  ))
  return { ...view, onDecide }
}

/** The content half of one dialog. */
function renderContent(dialog: DialogPrompt) {
  return render(() => <DialogRequestContent request={request} answerState={createControlAnswerState()} dialog={dialog} />)
}

describe('DialogRequestContent', () => {
  it('states the title and the message of a confirm', () => {
    const { container, getByText } = renderContent({ title: 'Proceed?', message: 'About to delete files.', variant: 'confirm' })
    expect(getByText('Proceed?')).toBeVisible()
    expect(getByText('About to delete files.')).toBeVisible()
    expect(container.querySelector('textarea')).toBeNull()
  })

  // The message belongs to a confirm, and the placeholder hint to an input. Each
  // variant draws only its own field, so a payload that carries both draws one.
  it('states the placeholder of an input as a hint and leaves its message out', () => {
    const { container, getByText, queryByText } = renderContent({
      title: 'Branch name',
      message: 'Unused',
      placeholder: 'main',
      variant: 'input',
    })
    expect(getByText('hint: main')).toBeVisible()
    expect(queryByText('Unused')).toBeNull()
    expect(container.querySelector('textarea')).toBeNull()
  })

  it('states no hint for an input with no placeholder', () => {
    const { container } = renderContent({ title: 'Branch name', variant: 'input' })
    expect(container.textContent).toBe('Branch name')
  })

  it('draws the editor with no message and no hint', () => {
    const { container, getByTestId } = renderContent({
      title: 'Commit message',
      message: 'Unused',
      placeholder: 'Unused',
      variant: 'editor',
    })
    expect(getByTestId('dialog-editor')).toHaveAccessibleName('Commit message')
    expect(container.textContent).toBe('Commit message')
  })

  it('starts an editor with no draft empty', () => {
    const { getByTestId } = renderContent({ title: 'Commit message', variant: 'editor' })
    expect((getByTestId('dialog-editor') as HTMLTextAreaElement).value).toBe('')
  })

  it('states the deadline of a dialog that resolves itself', () => {
    const { getByText } = renderContent({ title: 'Proceed?', variant: 'confirm', timeoutMs: 30_000 })
    expect(getByText('Auto-resolves in 30s if no response.')).toBeVisible()
  })

  it('states no deadline for a dialog that sets none', () => {
    const { queryByText } = renderContent({ title: 'Proceed?', variant: 'confirm' })
    expect(queryByText(/Auto-resolves/)).toBeNull()
  })
})

describe('DialogRequestActions', () => {
  it('answers a confirm with Approve and Deny', async () => {
    const { getByTestId, onDecide } = renderActions({ title: 'Proceed?', variant: 'confirm' })
    expect(getByTestId('control-allow-btn')).toHaveTextContent('Approve')
    expect(getByTestId('control-deny-btn')).toHaveTextContent('Deny')
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledTimes(2))
    expect(sent(onDecide)).toEqual([
      { action: DialogAction.CONFIRM, value: '' },
      { action: DialogAction.REFUSE, value: '' },
    ])
  })

  it('draws no field for a confirm or an editor', () => {
    expect(renderActions({ title: 'Proceed?', variant: 'confirm' }).queryByTestId('dialog-input')).toBeNull()
    expect(renderActions({ title: 'Message', variant: 'editor' }).queryByTestId('dialog-input')).toBeNull()
  })

  it('starts an input from its draft, and states its hint and its title', () => {
    const { getByTestId } = renderActions({
      title: 'Branch name',
      placeholder: 'main',
      prefill: 'feature/',
      variant: 'input',
    })
    const input = getByTestId('dialog-input') as HTMLInputElement
    expect(input.value).toBe('feature/')
    expect(input.placeholder).toBe('main')
    expect(input).toHaveAccessibleName('Branch name')
  })

  it('sends the typed text of an input, by the button and by Enter', async () => {
    const { getByTestId, onDecide } = renderActions({ title: 'Branch name', variant: 'input' })
    const input = getByTestId('dialog-input') as HTMLInputElement
    fireEvent.input(input, {
      target: { value: 'hello world' },
    })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    fireEvent.input(input, {
      target: { value: 'by enter' },
    })
    fireEvent.keyDown(input, { key: 'Enter' })
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledTimes(2))
    expect(sent(onDecide)).toEqual([
      { action: DialogAction.SUBMIT, value: 'hello world' },
      { action: DialogAction.SUBMIT, value: 'by enter' },
    ])
  })

  // A runtime tells an empty answer apart from a dismissal.
  it('sends an empty input as an empty value, and Cancel as a dismissal', async () => {
    const { getByTestId, onDecide } = renderActions({ title: 'Branch name', variant: 'input' })
    expect(getByTestId('control-allow-btn')).toHaveTextContent('Send')
    expect(getByTestId('control-deny-btn')).toHaveTextContent('Cancel')
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledTimes(2))
    expect(sent(onDecide)).toEqual([
      { action: DialogAction.SUBMIT, value: '' },
      { action: DialogAction.CANCEL, value: '' },
    ])
  })

  // The editor draws in the content half, and the send reads what the reader typed
  // there through the shared answer state.
  it('sends the text that the reader typed into the editor of the content half', async () => {
    const dialog: DialogPrompt = { title: 'Commit message', prefill: 'fix: ', variant: 'editor' }
    const answerState = createControlAnswerState()
    const content = render(() => <DialogRequestContent request={request} answerState={answerState} dialog={dialog} />)
    const editor = content.getByTestId('dialog-editor') as HTMLTextAreaElement
    expect(editor.value).toBe('fix: ')
    fireEvent.input(editor, {
      target: { value: 'fix: typo\nin the docs' },
    })
    const { getByTestId, onDecide } = renderActions(dialog, answerState)
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(sent(onDecide)).toEqual([
      { action: DialogAction.SUBMIT, value: 'fix: typo\nin the docs' },
    ])
  })

  it('sends the draft of an editor that the reader did not change', async () => {
    const { getByTestId, onDecide } = renderActions({ title: 'Commit message', prefill: 'fix: ', variant: 'editor' })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(sent(onDecide)).toEqual([
      { action: DialogAction.SUBMIT, value: 'fix: ' },
    ])
  })

  // A reader who clears the draft sends the empty text, not the draft again: the
  // answer state holds the empty value, and an empty value is a real answer.
  it('sends the empty text of an input whose draft the reader cleared', async () => {
    const { getByTestId, onDecide } = renderActions({ title: 'Branch name', prefill: 'feature/', variant: 'input' })
    fireEvent.input(getByTestId('dialog-input'), {
      target: { value: '' },
    })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(sent(onDecide)).toEqual([
      { action: DialogAction.SUBMIT, value: '' },
    ])
  })

  it('offers Cancel and Send for an editor, and Cancel dismisses it', async () => {
    const { getByTestId, onDecide } = renderActions({ title: 'Commit message', prefill: 'fix: ', variant: 'editor' })
    expect(getByTestId('control-allow-btn')).toHaveTextContent('Send')
    expect(getByTestId('control-deny-btn')).toHaveTextContent('Cancel')
    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(sent(onDecide)).toEqual([
      { action: DialogAction.CANCEL, value: '' },
    ])
  })

  it('sends nothing for a key other than Enter', () => {
    const { getByTestId, onDecide } = renderActions({ title: 'Branch name', variant: 'input' })
    const input = getByTestId('dialog-input')
    fireEvent.input(input, {
      target: { value: 'main' },
    })
    fireEvent.keyDown(input, { key: 'a' })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(onDecide).not.toHaveBeenCalled()
  })

  it('reports a send that fails from the Enter key', async () => {
    const failure = new Error('worker unreachable')
    const onDecide = vi.fn<ControlDecisionSender>(async () => {
      throw failure
    })
    vi.mocked(showWarnToast).mockClear()
    const { getByTestId } = render(() => (
      <DialogRequestActions
        request={request}
        dialog={{ title: 'Branch name', variant: 'input' }}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    fireEvent.keyDown(getByTestId('dialog-input'), { key: 'Enter' })
    await vi.waitFor(() => expect(showWarnToast).toHaveBeenCalledWith('Could not complete the action', failure))
  })
})

describe('ControlRequestActions', () => {
  // The Worker answers a dialog for every provider, so the banner draws the dialog's
  // own field and buttons whatever the provider is.
  it('draws the dialog\'s own field and sends its decision for any provider', async () => {
    const onDecide = vi.fn<ControlDecisionSender>(async () => {})
    const { getByTestId } = render(() => (
      <ControlRequestActions
        request={request}
        controlSurface={{
          kind: 'dialog',
          dialog: { title: 'Branch name', variant: 'input' },
        }}
        agentProvider={AgentProvider.CLAUDE_CODE}
        answerState={createControlAnswerState()}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    fireEvent.input(getByTestId('dialog-input'), {
      target: { value: 'main' },
    })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(sent(onDecide)).toEqual([
      { action: DialogAction.SUBMIT, value: 'main' },
    ])
  })
})
