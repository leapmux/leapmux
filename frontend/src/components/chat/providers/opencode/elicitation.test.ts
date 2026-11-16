import { describe, expect, it } from 'vitest'
import { createElicitationForm, elicitationFieldKey } from '../../controls/elicitationForm'
import { openCodeElicitation } from './elicitation'

describe('openCodeElicitation', () => {
  it.each([
    {
      key: 'empty',
      type: 'string',
      required: true,
      default: '',
    },
    {
      key: 'empty',
      type: 'multiselect',
      required: true,
      options: [],
      default: [],
    },
    {
      key: 'empty',
      type: 'string',
      options: [
        { value: 'allowed', label: 'Allowed' },
      ],
      default: 'other',
    },
  ])('rejects native values that the form does not admit: %j', (field) => {
    const request = openCodeElicitation({
      type: 'form.created',
      data: {
        form: {
          fields: [field],
        },
      },
    })
    expect(createElicitationForm(request?.schema, request?.fieldRules).read({}).error).toBeTruthy()
  })

  it('omits inactive stale answers and cascades inactivity to later fields', () => {
    const request = openCodeElicitation({
      type: 'form.created',
      data: {
        form: {
          fields: [
            { key: 'enabled', type: 'boolean', default: false },
            {
              key: 'details',
              type: 'string',
              required: true,
              when: [
                { key: 'enabled', op: 'eq', value: true },
              ],
            },
            {
              key: 'count',
              type: 'integer',
              required: true,
              when: [
                { key: 'details', op: 'neq', value: '' },
              ],
            },
            {
              key: 'hidden',
              type: 'integer',
              hidden: true,
              default: 0,
            },
          ],
        },
      },
    })
    const form = createElicitationForm(request?.schema, request?.fieldRules)
    const stale = {
      [elicitationFieldKey('details')]: 'previous answer',
      [elicitationFieldKey('count')]: 'invalid stale number',
    }
    expect(form.visibleFields(stale).map(field => field.key)).toEqual(['enabled'])
    expect(form.read(stale)).toEqual({
      content: { enabled: false, hidden: 0 },
    })
    expect(form.read({ [elicitationFieldKey('enabled')]: 'true' }).error).toBe('details is required.')
    const active = { [elicitationFieldKey('enabled')]: 'true', [elicitationFieldKey('details')]: 'yes', [elicitationFieldKey('count')]: '0' }
    expect(form.visibleFields(active).map(field => field.key)).toEqual(['enabled', 'details', 'count'])
    expect(form.read(active)).toEqual({
      content: {
        enabled: true,
        details: 'yes',
        count: 0,
        hidden: 0,
      },
    })
  })

  it.each(['eq', 'neq'])('does not activate a field when the condition source is unanswered for %s', (op) => {
    const request = openCodeElicitation({
      type: 'form.created',
      data: {
        form: {
          fields: [
            { key: 'source', type: 'string' },
            {
              key: 'dependent',
              type: 'string',
              required: true,
              when: [
                { key: 'source', op, value: 'chosen' },
              ],
            },
          ],
        },
      },
    })
    const form = createElicitationForm(request?.schema, request?.fieldRules)
    expect(form.read({})).toEqual({ content: {} })
    expect(form.visibleFields({}).map(field => field.key)).toEqual(['source'])
  })

  it('matches multiselect membership and preserves empty hidden defaults', () => {
    const request = openCodeElicitation({
      type: 'form.created',
      data: {
        form: {
          fields: [
            {
              key: 'options',
              type: 'multiselect',
              options: [
                { value: 'a', label: 'A' },
              ],
              default: ['a'],
            },
            {
              key: 'selected',
              type: 'string',
              default: '',
              hidden: true,
              when: [
                { key: 'options', op: 'eq', value: 'a' },
              ],
            },
            {
              key: 'other',
              type: 'boolean',
              default: false,
              when: [
                { key: 'options', op: 'neq', value: 'a' },
              ],
            },
          ],
        },
      },
    })
    const form = createElicitationForm(request?.schema, request?.fieldRules)
    expect(form.read({})).toEqual({
      content: {
        options: ['a'],
        selected: '',
      },
    })
    expect(form.read({ [elicitationFieldKey('options')]: '[]' })).toEqual({
      content: { options: [], other: false },
    })
  })

  it('requires a conditional field only when its earlier answer matches', () => {
    const request = openCodeElicitation({
      type: 'form.created',
      data: {
        form: {
          fields: [
            { key: 'enabled', type: 'boolean', default: false },
            {
              key: 'details',
              type: 'string',
              required: true,
              when: [
                { key: 'enabled', op: 'eq', value: true },
              ],
            },
          ],
        },
      },
    })
    expect(createElicitationForm(request?.schema, request?.fieldRules).read({})).toEqual({
      content: { enabled: false },
    })
  })

  it('preserves prototype-shaped property keys and typed defaults', () => {
    const request = openCodeElicitation({
      type: 'form.created',
      data: {
        form: {
          fields: [
            {
              key: '__proto__',
              type: 'integer',
              default: 0,
              required: true,
            },
            { key: 'constructor', type: 'boolean', default: false },
            { key: '', type: 'string', default: '' },
          ],
        },
      },
    })
    expect(request).toBeDefined()
    expect(createElicitationForm(request?.schema).read({})).toEqual({
      content: Object.fromEntries([
        ['__proto__', 0],
        ['constructor', false],
        ['', ''],
      ]),
    })
  })
})
