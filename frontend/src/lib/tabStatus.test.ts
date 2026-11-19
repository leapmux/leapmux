import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { TabStatusSchema, TabStatusTone } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { tabStatusDescription, tabStatusView } from './tabStatus'

describe('tabStatusView', () => {
  it('reads the text, the progress and the tone', () => {
    const status = create(TabStatusSchema, { text: 'Running the tests', progressPercent: 40, tone: TabStatusTone.WARNING })
    expect(tabStatusView(status)).toEqual({ text: 'Running the tests', progressPercent: 40, tone: 'warning' })
  })

  it('keeps an unset progress unset, so no ring is drawn', () => {
    expect(tabStatusView(create(TabStatusSchema, { text: 'Waiting' }))?.progressPercent).toBeUndefined()
  })

  // 0 is a progress: it draws an empty ring, not the dot.
  it('keeps a progress of 0', () => {
    expect(tabStatusView(create(TabStatusSchema, { text: 'Starting', progressPercent: 0 }))?.progressPercent).toBe(0)
  })

  it('caps a progress over 100 so the ring cannot overfill', () => {
    expect(tabStatusView(create(TabStatusSchema, { text: 'x', progressPercent: 250 }))?.progressPercent).toBe(100)
  })

  // The wire field is a uint32, so its largest value is the far end of what a
  // Worker that skips its own check can send.
  it.each([
    [99, 99],
    [100, 100],
    [101, 100],
    [0xFFFF_FFFF, 100],
  ])('reads a progress of %i as %i', (wire, drawn) => {
    expect(tabStatusView(create(TabStatusSchema, { text: 'x', progressPercent: wire }))?.progressPercent).toBe(drawn)
  })

  it.each([
    [TabStatusTone.UNSPECIFIED, 'neutral'],
    [TabStatusTone.SUCCESS, 'success'],
    [TabStatusTone.WARNING, 'warning'],
    [TabStatusTone.ERROR, 'error'],
    // A tone that a newer Worker added draws in the neutral color rather than
    // hiding the status.
    [99 as TabStatusTone, 'neutral'],
  ] as const)('reads tone %i as %s', (tone, name) => {
    expect(tabStatusView(create(TabStatusSchema, { text: 'x', tone }))?.tone).toBe(name)
  })

  it('reads an absent status, which is how a clear arrives, as none', () => {
    expect(tabStatusView(undefined)).toBeUndefined()
  })

  it.each(['', '  \n '])('reads a status with the text %j as none', (text) => {
    expect(tabStatusView(create(TabStatusSchema, { text, progressPercent: 10 }))).toBeUndefined()
  })

  it('trims the text', () => {
    expect(tabStatusView(create(TabStatusSchema, { text: '  Building\n' }))?.text).toBe('Building')
  })
})

describe('tabStatusDescription', () => {
  it('states the progress, which the ring alone shows', () => {
    expect(tabStatusDescription({ text: 'Migrating rows', progressPercent: 40, tone: 'neutral' })).toBe('Migrating rows (40%)')
    expect(tabStatusDescription({ text: 'Starting', progressPercent: 0, tone: 'neutral' })).toBe('Starting (0%)')
  })

  it('is the text alone without a progress', () => {
    expect(tabStatusDescription({ text: 'Waiting', progressPercent: undefined, tone: 'error' })).toBe('Waiting')
  })
})
