import type { ElicitationRequest } from '../model/controlPrompt'
import type { ControlDecisionSender } from './types'
import type { ElicitationDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ElicitationAction } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { ControlRequestActions, ControlRequestContent } from '~/test-support/controlRequestBanner'
import { testControlRequest } from '~/test-support/controlRequests'
import { createControlAnswerState } from './types'

const schema = {
  type: 'object',
  required: ['count', 'enabled', 'color'],
  properties: {
    count: {
      type: 'integer',
      title: 'Count',
      minimum: 0,
      maximum: 3,
    },
    enabled: { type: 'boolean', title: 'Enabled' },
    color: {
      type: 'string',
      title: 'Color',
      oneOf: [
        { const: 'b', title: 'Blue' },
        { const: 'r', title: 'Red' },
      ],
    },
  },
}

/** One elicitation request, with the prompt that the Worker reads from it. */
function elicitationRequest(requestId: string, elicitation: ElicitationRequest, fields: Partial<ControlRequest> = {}): ControlRequest {
  return testControlRequest({
    requestId,
    agentId: 'agent',
    prompt: { kind: 'elicitation', elicitation },
    ...fields,
  })
}

/** The elicitation decision of one call of the sender, with its content read back. */
function decided(onDecide: ReturnType<typeof vi.fn<ControlDecisionSender>>, call = 0): ElicitationDecision & { content: unknown } {
  const decision = onDecide.mock.calls[call]?.[0]
  if (decision?.kind.case !== 'elicitation')
    throw new Error(`call ${call} sent no elicitation decision`)
  const value = decision.kind.value
  return {
    ...value,
    content: value.contentJson === ''
      ? undefined
      : JSON.parse(value.contentJson),
  }
}

/** Both halves of the banner for one request, with a sender that records each decision. */
function renderBanner(request: ControlRequest, agentProvider: AgentProvider = AgentProvider.GOOSE, onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)) {
  const answerState = createControlAnswerState()
  render(() => (
    <>
      <ControlRequestContent request={request} answerState={answerState} />
      <ControlRequestActions request={request} answerState={answerState} agentProvider={agentProvider} onDecide={onDecide} hasEditorContent={false} onTriggerSend={() => {}} />
    </>
  ))
  return { onDecide, answerState }
}

describe('shared elicitation control', () => {
  // A hidden field keeps its default, and a field that a condition turns off
  // drops the answer that the reader typed while it was on.
  it('updates conditional fields and sends hidden defaults with typed answers', async () => {
    const { onDecide } = renderBanner(elicitationRequest('conditional-form', {
      mode: 'form',
      message: '',
      schema: {
        type: 'object',
        properties: {
          choice: { type: 'string', title: 'Choice' },
          details: { type: 'string', title: 'Details', minLength: 1 },
          count: { type: 'integer', title: 'Hidden count', default: 0 },
        },
        required: ['details'],
      },
      fieldRules: [
        { key: 'details', conditions: [{ field: 'choice', comparison: 'equal', value: 'yes' }] },
        { key: 'count', hidden: true },
      ],
    }))
    expect(screen.queryByLabelText('Hidden count')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Details *')).not.toBeInTheDocument()
    fireEvent.input(screen.getByLabelText('Choice'), {
      target: { value: 'yes' },
    })
    expect(screen.getByLabelText('Details *')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('Details *'), {
      target: { value: 'stale' },
    })
    fireEvent.input(screen.getByLabelText('Choice'), {
      target: { value: 'no' },
    })
    expect(screen.queryByLabelText('Details *')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    const decision = decided(onDecide)
    expect(decision.action).toBe(ElicitationAction.ACCEPT)
    expect(decision.content).toEqual({ choice: 'no', count: 0 })
  })

  // The prompt draws the form, so the composer's provider changes nothing.
  it.each([undefined, AgentProvider.CLAUDE_CODE, AgentProvider.REASONIX])('draws and answers the form when the composer passes the provider %s', async (agentProvider) => {
    const request = elicitationRequest('early-form', {
      mode: 'form',
      message: '',
      schema: {
        type: 'object',
        properties: {
          count: { type: 'integer', title: 'Count' },
        },
      },
    }, { agentProvider: AgentProvider.REASONIX })
    const answerState = createControlAnswerState()
    const onDecide = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent
          request={request}
          answerState={answerState}
          {...(agentProvider === undefined
            ? {}
            : { agentProvider })}
        />
        <ControlRequestActions
          request={request}
          answerState={answerState}
          {...(agentProvider === undefined
            ? {}
            : { agentProvider })}
          onDecide={onDecide}
          hasEditorContent={false}
          onTriggerSend={() => {}}
        />
      </>
    ))
    expect(screen.getByTestId('elicitation-form')).toBeVisible()
    fireEvent.input(screen.getByLabelText('Count'), {
      target: { value: '0' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(decided(onDecide)).toMatchObject({
      action: ElicitationAction.ACCEPT,
      content: { count: 0 },
    })
  })

  it('filters a long choice list and can clear an optional answer', async () => {
    const { onDecide } = renderBanner(elicitationRequest('choices', {
      mode: 'form',
      message: '',
      schema: {
        type: 'object',
        properties: {
          color: {
            type: 'string',
            title: 'Color',
            enum: [
              'blue',
              'red',
              'green',
              'orange',
              'yellow',
              'pink',
              'white',
              'black',
              'cyan',
              'magenta',
              'lime',
              'brown',
            ],
          },
        },
      },
    }))
    // JSDOM keeps popover children outside its accessibility tree. Browser tests check visibility.
    const trigger = screen.getByRole('button', { name: 'Color' })
    fireEvent.click(trigger)
    fireEvent.input(screen.getByRole('textbox', { name: 'Filter Color', hidden: true }), {
      target: { value: 'green' },
    })
    expect(screen.queryByRole('menuitemradio', { name: 'red', hidden: true })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'green', hidden: true }))
    expect(trigger).toHaveTextContent('green')
    fireEvent.click(trigger)
    fireEvent.input(screen.getByRole('textbox', { name: 'Filter Color', hidden: true }), {
      target: { value: 'no match' },
    })
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'Select an option', hidden: true }))
    expect(trigger).toHaveTextContent('Select an option')
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(decided(onDecide)).toMatchObject({ action: ElicitationAction.ACCEPT, content: {} })
  })

  it('shows a protected URL and sends approval without form values', async () => {
    const { onDecide } = renderBanner(elicitationRequest('url', {
      mode: 'url',
      message: 'Complete the provider setup.',
      url: 'https://example.com/setup?flow=1',
    }))
    const link = screen.getByRole('link', { name: 'https://example.com/setup?flow=1' })
    expect(link).toHaveAttribute('href', 'https://example.com/setup?flow=1')
    expect(link).toHaveAttribute(UNTRUSTED_LINK_ATTRIBUTE)
    expect(link).toHaveAttribute('rel', 'noopener noreferrer nofollow')
    expect(screen.queryByTestId('elicitation-form')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(decided(onDecide)).toMatchObject({ action: ElicitationAction.ACCEPT, contentJson: '', content: undefined })
  })

  it.each([
    'javascript:alert(1)',
    'file:///private/settings',
    'https://user:secret@example.com/',
    '',
  ])('refuses the unusable URL %s and permits rejection', async (url) => {
    const { onDecide } = renderBanner(elicitationRequest('unsafe-url', {
      mode: 'url',
      message: '',
      ...(url === ''
        ? {}
        : { url }),
    }))
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.getByText('The request contains no usable web link.')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(decided(onDecide)).toMatchObject({ action: ElicitationAction.DECLINE, contentJson: '' })
  })

  it('permits cancellation of an unknown interaction mode', async () => {
    const { onDecide } = renderBanner(elicitationRequest('future', {
      mode: 'future-mode',
      message: 'Unknown interaction',
    }))
    expect(screen.getByText(/This request type is not supported/)).toBeVisible()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'More actions' }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Cancel', hidden: true }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(decided(onDecide)).toMatchObject({ action: ElicitationAction.CANCEL, contentJson: '' })
  })

  it('validates multiple choices and omits an untouched optional field', async () => {
    const { onDecide } = renderBanner(elicitationRequest('multiple', {
      mode: 'form',
      message: '',
      schema: {
        type: 'object',
        required: ['colors'],
        properties: {
          colors: {
            type: 'array',
            title: 'Colors',
            minItems: 1,
            maxItems: 1,
            items: {
              type: 'string',
              enum: ['blue', 'red'],
            },
          },
          note: { type: 'string', title: 'Note' },
        },
      },
    }))
    const approve = screen.getByRole('button', { name: 'Approve' })
    expect(approve).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'blue' }))
    expect(approve).toBeEnabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'red', hidden: true }))
    expect(approve).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'blue' }))
    fireEvent.click(approve)
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    const decision = decided(onDecide)
    expect(decision).toMatchObject({
      action: ElicitationAction.ACCEPT,
      content: {
        colors: ['red'],
      },
    })
    expect(decision.contentJson).not.toContain('note')
  })

  // The prompt of an MCP tool approval, as the Worker reads Codex's.
  it('shows the tool arguments and sends the offered approval duration that the reader chose', async () => {
    const { onDecide } = renderBanner(elicitationRequest('mcp-approval', {
      purpose: 'permission',
      mode: 'form',
      message: 'Allow this tool?',
      schema: { type: 'object', properties: {} },
      arguments: { path: 'sample.py', count: 0 },
      acceptChoices: [
        { key: 'once', label: 'Once' },
        { key: 'session', label: 'Session' },
        { key: 'always', label: 'Always' },
      ],
    }), AgentProvider.CODEX)
    expect(screen.getByText(/sample.py/)).toBeVisible()
    fireEvent.click(screen.getByRole('radio', { name: 'Session' }))
    fireEvent.click(screen.getByRole('button', { name: 'Allow' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(decided(onDecide)).toMatchObject({ action: ElicitationAction.ACCEPT, acceptChoiceKey: 'session', content: {} })
  })

  it('sends the first accept choice when the reader picks none', async () => {
    const { onDecide } = renderBanner(elicitationRequest('mcp-approval', {
      purpose: 'permission',
      mode: 'form',
      message: 'Allow this tool?',
      schema: { type: 'object', properties: {} },
      acceptChoices: [
        { key: 'once', label: 'Once' },
        { key: 'always', label: 'Always' },
      ],
    }), AgentProvider.CODEX)
    fireEvent.click(screen.getByRole('button', { name: 'Allow' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(decided(onDecide).acceptChoiceKey).toBe('once')
  })

  it('answers the shared form with every field', async () => {
    const { onDecide } = renderBanner(elicitationRequest('001', { mode: 'form', message: 'Choose the settings.', schema }))
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('Count *'), {
      target: { value: '0' },
    })
    expect(screen.queryAllByRole('combobox')).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'Enabled *' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'No', hidden: true }))
    fireEvent.click(screen.getByRole('button', { name: 'Color *' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'Blue', hidden: true }))
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    expect(decided(onDecide)).toMatchObject({
      action: ElicitationAction.ACCEPT,
      content: { count: 0, enabled: false, color: 'b' },
      acceptChoiceKey: '',
    })
  })

  it('retains the form and permits retry after delivery fails', async () => {
    const onDecide = vi.fn<ControlDecisionSender>().mockRejectedValueOnce(new Error('offline')).mockResolvedValue(undefined)
    render(() => (
      <ControlRequestActions
        request={elicitationRequest('form', {
          mode: 'form',
          message: '',
          schema: { type: 'object', properties: {} },
        })}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.GOOSE}
        onDecide={onDecide}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Could not complete the response: offline'))
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledTimes(2))
    expect(decided(onDecide, 1)).toMatchObject({ action: ElicitationAction.DECLINE, contentJson: '' })
  })
})

// A send that RESOLVES does not always answer the request: the worker can record
// a response it cannot confirm, and the store then keeps the request open. The
// pending reset lived in the catch alone, so the buttons stayed disabled with no
// way to send again. See SCAN-S8-2.
describe('elicitationActions after a send that leaves the request open', () => {
  it('re-enables its decisions', async () => {
    const { onDecide } = renderBanner(elicitationRequest('open', {
      mode: 'form',
      message: 'Choose the settings.',
      schema: {
        type: 'object',
        properties: {
          count: { type: 'integer', title: 'Count' },
        },
      },
    }))
    fireEvent.input(screen.getByLabelText('Count'), {
      target: { value: '1' },
    })
    const approve = screen.getByRole('button', { name: 'Approve' })
    fireEvent.click(approve)
    await waitFor(() => expect(onDecide).toHaveBeenCalledOnce())
    await waitFor(() => expect(approve).toBeEnabled())
    expect(screen.getByRole('button', { name: 'Reject' })).toBeEnabled()
    fireEvent.click(approve)
    await waitFor(() => expect(onDecide).toHaveBeenCalledTimes(2))
  })
})
