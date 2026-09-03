import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { TabPayloadSchema } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { fileTabPayload, imageTabPayload, tabPayloadView } from './tabPayload'

describe('tabPayloadView', () => {
  it('decodes the file branch', () => {
    const payload = create(TabPayloadSchema, fileTabPayload('/repo/a.ts', '/repo'))
    expect(tabPayloadView(payload)).toEqual({ kind: 'file', filePath: '/repo/a.ts', workingDir: '/repo' })
  })

  it('decodes the image branch', () => {
    const payload = create(TabPayloadSchema, imageTabPayload({
      agentId: 'a1',
      seq: 42n,
      imageIndex: 1,
      title: 'Read',
      workingDir: '/repo',
    }))
    expect(tabPayloadView(payload)).toEqual({
      kind: 'image',
      agentId: 'a1',
      seq: 42n,
      imageIndex: 1,
      title: 'Read',
      workingDir: '/repo',
    })
  })

  it('returns null for an absent payload', () => {
    expect(tabPayloadView(undefined)).toBeNull()
  })

  it('returns null for a payload stating no kind', () => {
    // A tab kind a newer peer registered. Returning a half-built `file` view
    // would render it as an empty FILE tab, which is worse than saying nothing.
    expect(tabPayloadView(create(TabPayloadSchema, { workingDir: '/repo' }))).toBeNull()
  })
})
