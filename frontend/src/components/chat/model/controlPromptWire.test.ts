import type { MessageInitShape } from '@bufbuild/protobuf'
import type { ControlPrompt as WireControlPrompt } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  ControlEditorPurpose,
  ControlPayloadFault,
  ControlPromptSchema,
  DialogVariant,
  ElicitationFieldComparison,
  ElicitationPurpose,
  PermissionOptionKind,
  PermissionScope,
  PlanChoiceEffect,
} from '~/generated/proto/leapmux/v1/agent_control_pb'
import { controlEditorRoleFromWire, controlPayloadFaultFromWire, controlPromptFromWire } from './controlPromptWire'

/** The kind of a prompt, as `create` takes it. */
type PromptKind = NonNullable<Exclude<MessageInitShape<typeof ControlPromptSchema>, WireControlPrompt>['kind']>

function prompt(kind: PromptKind, editorPurpose = ControlEditorPurpose.UNSPECIFIED): WireControlPrompt {
  return create(ControlPromptSchema, { kind, editorPurpose })
}

describe('controlPromptFromWire', () => {
  it('reads a request with no prompt as a permission with no options', () => {
    expect(controlPromptFromWire(undefined)).toEqual({
      kind: 'permission',
      permission: { options: [] },
    })
    expect(controlPromptFromWire(create(ControlPromptSchema, {}))).toEqual({
      kind: 'permission',
      permission: { options: [] },
    })
  })

  it('reads a permission with its options, its input and only the text it states', () => {
    const read = controlPromptFromWire(prompt({
      case: 'permission',
      value: {
        title: 'Bash',
        command: 'ls',
        inputJson: '{"command":"ls"}',
        independentOptions: true,
        options: [
          { optionId: 'once', kind: PermissionOptionKind.ALLOW_ONCE, name: 'Allow' },
          {
            optionId: 'always',
            kind: PermissionOptionKind.ALLOW_ALWAYS,
            name: 'Always',
            scope: PermissionScope.PROJECT,
          },
          { optionId: 'no', kind: PermissionOptionKind.REJECT_ONCE, endsTurn: true },
          { optionId: 'x', kind: PermissionOptionKind.OTHER, name: 'Ask later' },
        ],
      },
    }))
    expect(read).toEqual({
      kind: 'permission',
      permission: {
        title: 'Bash',
        command: 'ls',
        input: { command: 'ls' },
        independentOptions: true,
        options: [
          { optionId: 'once', kind: 'allow_once', name: 'Allow' },
          {
            optionId: 'always',
            kind: 'allow_always',
            name: 'Always',
            scope: 'project',
          },
          { optionId: 'no', kind: 'reject_once', endsTurn: true },
          { optionId: 'x', kind: 'other', name: 'Ask later' },
        ],
      },
    })
  })

  it('reads the scope label of an option, and an empty one as absent', () => {
    const read = controlPromptFromWire(prompt({
      case: 'permission',
      value: {
        options: [
          {
            optionId: 'host',
            kind: PermissionOptionKind.ALLOW_ALWAYS,
            scope: PermissionScope.USER,
            scopeLabel: 'Host rule',
          },
          {
            optionId: 'block',
            kind: PermissionOptionKind.REJECT_ALWAYS,
            scope: PermissionScope.USER,
            scopeLabel: 'Host rule',
          },
          {
            optionId: 'session',
            kind: PermissionOptionKind.ALLOW_ALWAYS,
            scope: PermissionScope.SESSION,
            scopeLabel: '',
          },
        ],
      },
    }))
    expect(read).toEqual({
      kind: 'permission',
      permission: {
        options: [
          {
            optionId: 'host',
            kind: 'allow_always',
            scope: 'user',
            scopeLabel: 'Host rule',
          },
          {
            optionId: 'block',
            kind: 'reject_always',
            scope: 'user',
            scopeLabel: 'Host rule',
          },
          { optionId: 'session', kind: 'allow_always', scope: 'session' },
        ],
      },
    })
  })

  it('keeps an input that is not JSON as its text', () => {
    const read = controlPromptFromWire(prompt({
      case: 'permission',
      value: { inputJson: 'not json', options: [] },
    }))
    expect(read).toEqual({
      kind: 'permission',
      permission: { input: 'not json', options: [] },
    })
  })

  it('reads the effect of each plan choice, and an unstated effect as other', () => {
    const read = controlPromptFromWire(prompt({
      case: 'plan',
      value: {
        text: 'Step 1',
        choices: [
          { id: 'a', label: 'Approve', effect: PlanChoiceEffect.APPROVE },
          {
            id: 'r',
            label: 'Refuse',
            effect: PlanChoiceEffect.REFUSE,
            description: 'Keep planning',
          },
          { id: 'u', label: 'Later', effect: PlanChoiceEffect.UNSPECIFIED },
        ],
      },
    }))
    expect(read).toEqual({
      kind: 'plan',
      text: 'Step 1',
      choices: [
        { id: 'a', label: 'Approve', effect: 'approve' },
        {
          id: 'r',
          label: 'Refuse',
          effect: 'refuse',
          description: 'Keep planning',
        },
        { id: 'u', label: 'Later', effect: 'other' },
      ],
    })
  })

  it('reads a question with its context, its counts and its option values', () => {
    const read = controlPromptFromWire(prompt({
      case: 'question',
      value: {
        preservesSelectionNotes: true,
        questions: [
          {
            id: 'q1',
            header: 'Extras',
            context: 'The build runs nightly.',
            question: 'Which extras?',
            multiSelect: true,
            allowEmpty: true,
            allowCustomText: false,
            minimumSelections: 1,
            maximumSelections: 2,
            options: [
              { label: 'Cache', value: 'cache', description: 'Faster' },
              { label: 'Logs' },
            ],
          },
        ],
      },
    }))
    expect(read).toEqual({
      kind: 'question',
      preservesSelectionNotes: true,
      questions: [
        {
          id: 'q1',
          header: 'Extras',
          context: 'The build runs nightly.',
          question: 'Which extras?',
          multiSelect: true,
          allowEmpty: true,
          allowCustomText: false,
          minimumSelections: 1,
          maximumSelections: 2,
          options: [
            { label: 'Cache', value: 'cache', description: 'Faster' },
            { label: 'Logs' },
          ],
        },
      ],
    })
  })

  it('tells an unset initial selection from an empty one, which is an answer', () => {
    const questionsOf = (initialSelections?: { values: string[] }) => {
      const read = controlPromptFromWire(prompt({
        case: 'question',
        value: {
          questions: [
            {
              question: 'Pick',
              options: [
                { label: 'A' },
              ],
              ...(initialSelections
                ? { initialSelections }
                : {}),
            },
          ],
        },
      }))
      return read.kind === 'question'
        ? read.questions
        : []
    }
    expect(questionsOf()[0]).not.toHaveProperty('initialSelections')
    expect(questionsOf({ values: [] })[0]?.initialSelections).toEqual([])
    expect(questionsOf({
      values: ['A'],
    })[0]?.initialSelections).toEqual(['A'])
  })

  it('reads the field rules of a form, and drops a condition that states nothing', () => {
    const read = controlPromptFromWire(prompt({
      case: 'elicitation',
      value: {
        mode: 'form',
        message: 'Deploy',
        schemaJson: '{"type":"object","properties":{"env":{"type":"string"}}}',
        fieldRules: [
          { key: 'token', hidden: true },
          {
            key: 'region',
            conditions: [
              { field: 'env', comparison: ElicitationFieldComparison.EQUAL, valueJson: '"prod"' },
              { field: 'replicas', comparison: ElicitationFieldComparison.NOT_EQUAL, valueJson: '0' },
              { field: 'dry', comparison: ElicitationFieldComparison.EQUAL, valueJson: 'false' },
              { field: 'env', comparison: ElicitationFieldComparison.UNSPECIFIED, valueJson: '"dev"' },
              { field: 'env', comparison: ElicitationFieldComparison.EQUAL, valueJson: '["prod"]' },
              { field: 'env', comparison: ElicitationFieldComparison.EQUAL, valueJson: 'prod' },
              { field: 'env', comparison: ElicitationFieldComparison.EQUAL, valueJson: '' },
            ],
          },
          { key: 'plain' },
        ],
      },
    }))
    expect(read).toEqual({
      kind: 'elicitation',
      elicitation: {
        mode: 'form',
        message: 'Deploy',
        schema: {
          type: 'object',
          properties: {
            env: { type: 'string' },
          },
        },
        fieldRules: [
          { key: 'token', hidden: true },
          {
            key: 'region',
            conditions: [
              { field: 'env', comparison: 'equal', value: 'prod' },
              { field: 'replicas', comparison: 'not-equal', value: 0 },
              { field: 'dry', comparison: 'equal', value: false },
            ],
          },
          { key: 'plain' },
        ],
      },
    })
  })

  it('reads a form with no field rules without the member', () => {
    const read = controlPromptFromWire(prompt({
      case: 'elicitation',
      value: {
        mode: 'url',
        message: 'Sign in',
        url: 'https://example.com',
        purpose: ElicitationPurpose.PERMISSION,
        acceptChoices: [
          { key: 's', label: 'Session' },
        ],
      },
    }))
    expect(read).toEqual({
      kind: 'elicitation',
      elicitation: {
        mode: 'url',
        message: 'Sign in',
        url: 'https://example.com',
        purpose: 'permission',
        acceptChoices: [
          { key: 's', label: 'Session' },
        ],
      },
    })
  })

  it('reads a dialog of no stated variant as a confirm, with its timeout', () => {
    expect(controlPromptFromWire(prompt({
      case: 'dialog',
      value: { title: 'Go?', timeoutMs: 5000n },
    })))
      .toEqual({
        kind: 'dialog',
        dialog: { title: 'Go?', variant: 'confirm', timeoutMs: 5000 },
      })
    expect(controlPromptFromWire(prompt({
      case: 'dialog',
      value: { title: 'Name', variant: DialogVariant.INPUT, placeholder: 'main' },
    })))
      .toEqual({
        kind: 'dialog',
        dialog: { title: 'Name', variant: 'input', placeholder: 'main' },
      })
  })
})

describe('controlPayloadFaultFromWire', () => {
  it('reads the fault of an unreadable payload, and none for a readable one', () => {
    expect(controlPayloadFaultFromWire(prompt({ case: 'unreadable', value: ControlPayloadFault.NOT_AN_OBJECT }))).toBe('not-an-object')
    expect(controlPayloadFaultFromWire(prompt({ case: 'unreadable', value: ControlPayloadFault.MALFORMED }))).toBe('malformed')
    expect(controlPayloadFaultFromWire(prompt({
      case: 'permission',
      value: { options: [] },
    }))).toBeUndefined()
    expect(controlPayloadFaultFromWire(undefined)).toBeUndefined()
  })
})

describe('controlEditorRoleFromWire', () => {
  it('reads each purpose, and an unstated one as feedback', () => {
    expect(controlEditorRoleFromWire(prompt({ case: undefined }, ControlEditorPurpose.ANSWER))).toBe('answer')
    expect(controlEditorRoleFromWire(prompt({ case: undefined }, ControlEditorPurpose.NONE))).toBe('none')
    expect(controlEditorRoleFromWire(prompt({ case: undefined }, ControlEditorPurpose.FEEDBACK))).toBe('feedback')
    expect(controlEditorRoleFromWire(prompt({ case: undefined }))).toBe('feedback')
    expect(controlEditorRoleFromWire(undefined)).toBe('feedback')
  })
})
