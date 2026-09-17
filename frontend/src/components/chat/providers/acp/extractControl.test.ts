import { describe, expect, it } from 'vitest'
import { acpExtractControl, acpPermissionSpanId } from './extractControl'

const TOOL_CALL = { toolCallId: 'call-1', kind: 'execute', title: 'Run', rawInput: { command: 'ls -al' } }
const OPTIONS = [
  { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
  { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
]

describe('acpExtractControl', () => {
  it('reads the tool call and the options of a full permission request', () => {
    const surface = acpExtractControl({ payload: { params: { toolCall: TOOL_CALL, options: OPTIONS } } })
    expect(surface).toEqual({
      kind: 'permission',
      permission: { title: 'Run', input: { command: 'ls -al' }, command: 'ls -al', options: OPTIONS },
    })
  })

  /*
   * A request that states its ANSWERS and no tool call is still a permission, and the
   * option ids are the only thing its reply can carry. Reading it as anything else
   * answers it with the wrong envelope -- the daemon maps an unknown reply to reject,
   * so an Allow would refuse the call.
   */
  it('reads a request that states options and no tool call', () => {
    const surface = acpExtractControl({ payload: { params: { options: OPTIONS } } })
    if (surface?.kind !== 'permission')
      throw new Error('an option list is a permission')
    expect(surface.permission.options).toEqual(OPTIONS)
    expect(surface.permission.title).toBe('')
    expect(surface.permission.command).toBeUndefined()
  })

  it('reads a tool call that states no options at all', () => {
    const surface = acpExtractControl({ payload: { params: { toolCall: TOOL_CALL } } })
    if (surface?.kind !== 'permission')
      throw new Error('a tool call is a permission')
    expect(surface.permission.options).toEqual([])
  })

  // Neither half means it is some other request, and the caller draws the payload.
  it('reads nothing out of a payload with neither half', () => {
    expect(acpExtractControl({ payload: { params: { sessionId: 's' } } })).toBeNull()
    expect(acpExtractControl({ payload: {} })).toBeNull()
  })

  /*
   * `options` is wire data, and six providers share this reader. A STRING was the
   * quiet case: its `length` is the length of the string, so the payload read as a
   * permission request, the layout walked its CHARACTERS, and the decision row drew
   * one empty button for each of them.
   */
  it.each([
    ['a string', 'allow_once'],
    ['a number', 7],
    ['an object', { optionId: 'once' }],
    ['a boolean', true],
  ])('reads no permission out of an options field that holds %s', (_shape, options) => {
    expect(acpExtractControl({ payload: { params: { options } } })).toBeNull()
  })

  // An element that is no object states no kind, and every reader of the layout
  // dereferences `option.kind` with no guard of its own.
  it('drops an element that cannot state a kind and keeps the ones that can', () => {
    const surface = acpExtractControl({ payload: { params: { options: [null, 'allow_once', 42, OPTIONS[0], []] } } })
    if (surface?.kind !== 'permission')
      throw new Error('one readable option is a permission')
    expect(surface.permission.options).toStrictEqual([{ optionId: 'once', kind: 'allow_once', name: 'Allow once' }])
  })

  // A tool call carries the request on its own, so a whole option list of unreadable
  // elements still leaves a permission -- with no option, which the shared Allow/Deny
  // pair answers.
  it('reads a tool call whose every option is unreadable', () => {
    const surface = acpExtractControl({ payload: { params: { toolCall: TOOL_CALL, options: [null, 'reject'] } } })
    if (surface?.kind !== 'permission')
      throw new Error('a tool call is a permission')
    expect(surface.permission.options).toStrictEqual([])
  })

  // Each field is READ, so a non-string leaves the option answerable rather than
  // handing a number to `permissionOptionLabel`. An empty name is no name: its kind
  // fallback states the truthful words instead.
  it('reads each option field and drops a name that states nothing', () => {
    const surface = acpExtractControl({
      payload: { params: { options: [{ optionId: 'once', kind: 'allow_once', name: '' }, { optionId: 7, kind: null, name: { text: 'x' } }] } },
    })
    if (surface?.kind !== 'permission')
      throw new Error('an option list is a permission')
    expect(surface.permission.options).toStrictEqual([
      { optionId: 'once', kind: 'allow_once' },
      { optionId: '', kind: '' },
    ])
  })

  // The arguments of a compact tool call live on that call's own transcript row, which
  // the banner loads and passes back in. Without it the banner showed a title alone.
  it('merges the arguments of the row the request points at', () => {
    const surface = acpExtractControl({
      payload: { params: { toolCall: { toolCallId: 'call-1', kind: 'execute', title: 'Run' } } },
      request: { parentObject: { toolCallId: 'call-1', rawInput: { command: 'ls -al' } } } as never,
    })
    if (surface?.kind !== 'permission')
      throw new Error('a tool call is a permission')
    expect(surface.permission.command).toBe('ls -al')
  })
})

describe('acpPermissionSpanId', () => {
  it('states the id of the tool call the banner must load', () => {
    expect(acpPermissionSpanId({ params: { toolCall: TOOL_CALL } })).toBe('call-1')
  })

  it('states an empty id for a request that carries no tool call', () => {
    expect(acpPermissionSpanId({ params: { options: OPTIONS } })).toBe('')
  })
})
