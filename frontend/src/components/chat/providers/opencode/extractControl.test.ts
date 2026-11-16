import { describe, expect, it } from 'vitest'
import { openCodeExtractControl } from './extractControl'

describe('openCodeExtractControl', () => {
  it('retains optional native permission metadata without dropping empty values', () => {
    const payload = {
      type: 'permission.asked',
      data: {
        action: 'shell',
        resources: ['printf hello'],
        save: [],
        metadata: { timeout: 0, quiet: false, note: '' },
      },
    }
    const original = structuredClone(payload)
    expect(openCodeExtractControl({ payload })).toMatchObject({
      kind: 'permission',
      permission: {
        command: 'printf hello',
        input: { timeout: 0, quiet: false, note: '' },
      },
    })
    expect(payload).toEqual(original)
  })

  it('keeps the requested resources authoritative over metadata', () => {
    expect(openCodeExtractControl({
      payload: {
        type: 'permission.asked',
        data: {
          action: 'external_directory',
          resources: ['/outside/*'],
          save: ['/outside/*'],
          metadata: {
            resources: ['unrelated'],
            detail: 'Inspect the directory.',
          },
        },
      },
    })).toMatchObject({
      permission: {
        input: {
          resources: ['/outside/*'],
          detail: 'Inspect the directory.',
        },
      },
    })
  })
})
