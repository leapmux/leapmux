import type { DialogPrompt } from '../model/controlPrompt'
import type { DialogResponder } from './DialogRequestControl'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { showWarnToast } from '~/components/common/Toast'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestActions } from '../ControlRequestBanner'
import { DialogRequestActions, DialogRequestContent, dialogTimeoutHint } from './DialogRequestControl'
import { createControlAnswerState } from './types'

// The Enter key of an input sends outside the decision buttons, so its failure
// reaches the reader through the shared action toast. The mock records it.
vi.mock('~/components/common/Toast', () => ({ showWarnToast: vi.fn() }))

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

/** A responder whose envelopes name the answer, so a test reads what was sent. */
const responder: DialogResponder = {
  confirm: (id, confirmed) => ({ id, confirmed }),
  value: (id, value) => ({ id, value }),
  cancel: id => ({ id, cancelled: true }),
}

const request: ControlRequest = { requestId: 'd1', agentId: 'agent-1', payload: {} }

/** The envelopes that each call of a sender carried. */
function sent(onRespond: ReturnType<typeof vi.fn>): unknown[] {
  return onRespond.mock.calls.map(call => JSON.parse(new TextDecoder().decode(call[0] as Uint8Array)))
}

/** The actions of one dialog, with a sender that records each envelope. */
function renderActions(dialog: DialogPrompt, answerState = createControlAnswerState()) {
  const onRespond = vi.fn(async (_content: Uint8Array) => {})
  const view = render(() => (
    <DialogRequestActions
      request={request}
      dialog={dialog}
      responder={responder}
      answerState={answerState}
      onRespond={onRespond}
      hasEditorContent={false}
      onTriggerSend={() => {}}
    />
  ))
  return { ...view, onRespond }
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
    const { container, getByText, queryByText } = renderContent({ title: 'Branch name', message: 'Unused', placeholder: 'main', variant: 'input' })
    expect(getByText('hint: main')).toBeVisible()
    expect(queryByText('Unused')).toBeNull()
    expect(container.querySelector('textarea')).toBeNull()
  })

  it('states no hint for an input with no placeholder', () => {
    const { container } = renderContent({ title: 'Branch name', variant: 'input' })
    expect(container.textContent).toBe('Branch name')
  })

  it('draws the editor with no message and no hint', () => {
    const { container, getByTestId } = renderContent({ title: 'Commit message', message: 'Unused', placeholder: 'Unused', variant: 'editor' })
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
    const { getByTestId, onRespond } = renderActions({ title: 'Proceed?', variant: 'confirm' })
    expect(getByTestId('control-allow-btn')).toHaveTextContent('Approve')
    expect(getByTestId('control-deny-btn')).toHaveTextContent('Deny')
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledTimes(2))
    expect(sent(onRespond)).toEqual([{ id: 'd1', confirmed: true }, { id: 'd1', confirmed: false }])
  })

  it('draws no field for a confirm or an editor', () => {
    expect(renderActions({ title: 'Proceed?', variant: 'confirm' }).queryByTestId('dialog-input')).toBeNull()
    expect(renderActions({ title: 'Message', variant: 'editor' }).queryByTestId('dialog-input')).toBeNull()
  })

  it('starts an input from its draft, and states its hint and its title', () => {
    const { getByTestId } = renderActions({ title: 'Branch name', placeholder: 'main', prefill: 'feature/', variant: 'input' })
    const input = getByTestId('dialog-input') as HTMLInputElement
    expect(input.value).toBe('feature/')
    expect(input.placeholder).toBe('main')
    expect(input).toHaveAccessibleName('Branch name')
  })

  it('sends the typed text of an input, by the button and by Enter', async () => {
    const { getByTestId, onRespond } = renderActions({ title: 'Branch name', variant: 'input' })
    const input = getByTestId('dialog-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'hello world' } })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    fireEvent.input(input, { target: { value: 'by enter' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledTimes(2))
    expect(sent(onRespond)).toEqual([{ id: 'd1', value: 'hello world' }, { id: 'd1', value: 'by enter' }])
  })

  // A runtime tells an empty answer apart from a dismissal.
  it('sends an empty input as an empty value, and Cancel as a dismissal', async () => {
    const { getByTestId, onRespond } = renderActions({ title: 'Branch name', variant: 'input' })
    expect(getByTestId('control-allow-btn')).toHaveTextContent('Send')
    expect(getByTestId('control-deny-btn')).toHaveTextContent('Cancel')
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledTimes(2))
    expect(sent(onRespond)).toEqual([{ id: 'd1', value: '' }, { id: 'd1', cancelled: true }])
  })

  // The editor draws in the content half, and the send reads what the reader typed
  // there through the shared answer state.
  it('sends the text that the reader typed into the editor of the content half', async () => {
    const dialog: DialogPrompt = { title: 'Commit message', prefill: 'fix: ', variant: 'editor' }
    const answerState = createControlAnswerState()
    const content = render(() => <DialogRequestContent request={request} answerState={answerState} dialog={dialog} />)
    const editor = content.getByTestId('dialog-editor') as HTMLTextAreaElement
    expect(editor.value).toBe('fix: ')
    fireEvent.input(editor, { target: { value: 'fix: typo\nin the docs' } })
    const { getByTestId, onRespond } = renderActions(dialog, answerState)
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sent(onRespond)).toEqual([{ id: 'd1', value: 'fix: typo\nin the docs' }])
  })

  it('sends the draft of an editor that the reader did not change', async () => {
    const { getByTestId, onRespond } = renderActions({ title: 'Commit message', prefill: 'fix: ', variant: 'editor' })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sent(onRespond)).toEqual([{ id: 'd1', value: 'fix: ' }])
  })

  // A reader who clears the draft sends the empty text, not the draft again: the
  // answer state holds the empty value, and an empty value is a real answer.
  it('sends the empty text of an input whose draft the reader cleared', async () => {
    const { getByTestId, onRespond } = renderActions({ title: 'Branch name', prefill: 'feature/', variant: 'input' })
    fireEvent.input(getByTestId('dialog-input'), { target: { value: '' } })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sent(onRespond)).toEqual([{ id: 'd1', value: '' }])
  })

  it('offers Cancel and Send for an editor, and Cancel dismisses it', async () => {
    const { getByTestId, onRespond } = renderActions({ title: 'Commit message', prefill: 'fix: ', variant: 'editor' })
    expect(getByTestId('control-allow-btn')).toHaveTextContent('Send')
    expect(getByTestId('control-deny-btn')).toHaveTextContent('Cancel')
    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sent(onRespond)).toEqual([{ id: 'd1', cancelled: true }])
  })

  it('sends nothing for a key other than Enter', () => {
    const { getByTestId, onRespond } = renderActions({ title: 'Branch name', variant: 'input' })
    const input = getByTestId('dialog-input')
    fireEvent.input(input, { target: { value: 'main' } })
    fireEvent.keyDown(input, { key: 'a' })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(onRespond).not.toHaveBeenCalled()
  })

  it('reports a send that fails from the Enter key', async () => {
    const failure = new Error('worker unreachable')
    const onRespond = vi.fn(async (_content: Uint8Array) => {
      throw failure
    })
    vi.mocked(showWarnToast).mockClear()
    const { getByTestId } = render(() => (
      <DialogRequestActions
        request={request}
        dialog={{ title: 'Branch name', variant: 'input' }}
        responder={responder}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    fireEvent.keyDown(getByTestId('dialog-input'), { key: 'Enter' })
    await vi.waitFor(() => expect(showWarnToast).toHaveBeenCalledWith('Could not complete the action', failure))
  })

  // Each answer carries the id of the request that the actions draw now, not the
  // id that they drew first.
  it('answers with the id of the current request', async () => {
    const [current, setCurrent] = createSignal<ControlRequest>(request)
    const onRespond = vi.fn(async (_content: Uint8Array) => {})
    const { getByTestId } = render(() => (
      <DialogRequestActions
        request={current()}
        dialog={{ title: 'Proceed?', variant: 'confirm' }}
        responder={responder}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    setCurrent({ ...request, requestId: 'd2' })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sent(onRespond)).toEqual([{ id: 'd2', confirmed: true }])
  })
})

describe('ControlRequestActions', () => {
  // Claude Code sends no dialog and states no responder. A dialog surface for it
  // still draws buttons, because a request with none would block the turn.
  it('draws the fallback pair for a dialog of a provider that states no responder', () => {
    const { getByTestId, queryByTestId } = render(() => (
      <ControlRequestActions
        request={request}
        controlSurface={{ kind: 'dialog', dialog: { title: 'Proceed?', variant: 'input' } }}
        agentProvider={AgentProvider.CLAUDE_CODE}
        answerState={createControlAnswerState()}
        onRespond={vi.fn(async () => {})}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    expect(queryByTestId('dialog-input')).toBeNull()
    expect(getByTestId('control-allow-btn')).toBeInTheDocument()
  })
})
