import { describe, expect, it } from 'vitest'
import { isObject } from '~/lib/jsonPick'
import { resolveOpenCodeMessage } from './resolveMessage'

describe('resolveOpenCodeMessage', () => {
  const scope = { sessionID: 'ses_1', assistantMessageID: 'msg_1', id: 'call_1' }
  const original = {
    type: 'session.tool.success',
    data: {
      ...scope,
      content: [
        { type: 'text', text: 'result' },
      ],
      executed: false,
    },
  }
  const parsed = {
    topLevel: original,
    parentObject: original,
    wrapper: null,
    rawText: '',
  }

  it('merges native name and input without changing either retained source', () => {
    const supplementalContent = [
      {
        type: 'session.tool.input.started',
        data: { ...scope, name: 'shell' },
      },
      {
        type: 'session.tool.called',
        data: {
          ...scope,
          input: { command: 'pwd', timeout: 0, background: false },
          executed: false,
        },
      },
    ]
    const before = JSON.stringify({ original, supplementalContent })
    expect(resolveOpenCodeMessage({ ...parsed, supplementalContent })).toEqual({
      ...original,
      data: {
        ...original.data,
        name: 'shell',
        input: { command: 'pwd', timeout: 0, background: false },
      },
    })
    expect(JSON.stringify({ original, supplementalContent })).toBe(before)
  })

  it.each([
    { sessionID: 'ses_other' },
    { assistantMessageID: 'msg_other' },
    { id: 'call_other' },
  ])('ignores a supplement from a different scope: $sessionID $assistantMessageID $id', (other) => {
    expect(resolveOpenCodeMessage({
      ...parsed,
      supplementalContent: [
        {
          type: 'session.tool.input.started',
          data: {
            ...scope,
            ...other,
            name: 'foreign',
          },
        },
      ],
    })).toBeUndefined()
  })

  it('keeps original fields when a supplement supplies an older value', () => {
    const parentObject = {
      ...original,
      data: {
        ...original.data,
        name: 'read',
        input: { path: '/current' },
      },
    }
    expect(resolveOpenCodeMessage({
      ...parsed,
      parentObject,
      supplementalContent: [
        {
          type: 'session.tool.input.started',
          data: { ...scope, name: 'shell' },
        },
        {
          type: 'session.tool.called',
          data: {
            ...scope,
            input: { path: '/old' },
          },
        },
      ],
    })).toEqual(parentObject)
  })

  it('reads retained progress metadata while preserving final native fields', () => {
    const parentObject = {
      ...original,
      data: {
        ...original.data,
        metadata: { exit: 0 },
      },
    }
    expect(resolveOpenCodeMessage({
      ...parsed,
      parentObject,
      supplementalContent: [
        {
          type: 'session.tool.progress',
          data: {
            ...scope,
            metadata: { output: 'live output', exit: 1 },
          },
        },
      ],
    })).toEqual({
      ...parentObject,
      data: {
        ...parentObject.data,
        metadata: { output: 'live output', exit: 0 },
      },
    })
  })

  it('copies prototype-shaped arguments as data properties', () => {
    const input = JSON.parse('{"__proto__":{"command":"inherited"},"timeout":0}')
    const resolved = resolveOpenCodeMessage({
      ...parsed,
      supplementalContent: [
        {
          type: 'session.tool.called',
          data: { ...scope, input },
        },
      ],
    })
    expect(resolved?.data).toEqual({ ...original.data, input })
    const data = resolved?.data
    if (!isObject(data) || !isObject(data.input))
      throw new Error('The resolved call must retain its argument object.')
    expect(Object.getPrototypeOf(data.input)).toBe(Object.prototype)
    expect(Object.hasOwn(data.input, '__proto__')).toBe(true)
    expect(data.input.command).toBeUndefined()
    expect(Object.getPrototypeOf(input)).toBe(Object.prototype)
    expect(input.command).toBeUndefined()
  })

  it('ignores unscoped and non-event payloads', () => {
    for (const parentObject of [
      { type: 'tool', id: 'c', state: {} },
      {
        type: 'session.tool.success',
        data: { id: 'call_1' },
      },
    ]) {
      expect(resolveOpenCodeMessage({ ...parsed, parentObject, supplementalContent: [] })).toBeUndefined()
    }
  })
})
