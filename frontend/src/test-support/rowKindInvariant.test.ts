import type { MessageCategory } from '~/components/chat/messageClassification'
import { describe, expect, it } from 'vitest'
import { classifyMessage } from '~/components/chat/messageClassification'
import { providerFor } from '~/components/chat/providers/registry'
import { input } from '~/components/chat/providers/testUtils'
import { extractChatRow } from '~/components/chat/rowExtraction'
import { drawnRowKind, ROW_KIND_CASES, ROW_KIND_FOR_CATEGORY } from '~/test-support/rowKindCorpus'
// Side-effect imports: the sweep below reads every provider out of the registry.
import '~/components/chat/providers'

// The invariant that keeps a MEASURED row and a DRAWN row the same row.
//
// Two readers take two different answers from layer 1. The virtual list premeasures
// a row's height from the CATEGORY that `classify` answers, before anything draws;
// the transcript then draws it from the row IR that `extractRow` answers. A frame
// the two disagree about reserves the height of one kind of row and paints another
// one into it, which reads as a scroll jump on a row nobody touched.
//
// It is a real risk and not a theoretical one, because the two live in separate
// functions and a provider adds a wire name to one of them at a time. A `hidden`
// category with a drawn row leaves a gap; a drawn category with a `hidden` row leaves
// a blank measured band.

describe('classification and extraction agree on every row kind', () => {
  // The map is a `Record<MessageCategory['kind'], ...>`, so the COMPILER already keeps
  // it total and a count of its keys asserts nothing. What no type can state is that
  // the corpus exercises each key. A category with no case let a provider add a wire
  // name to `classify` and not to `extractRow` and still ship green, which is the one
  // mistake this file exists to stop.
  it('exercises every classification the chat view can produce', () => {
    const covered = new Set<MessageCategory['kind']>(ROW_KIND_CASES.map(entry => entry.category))
    const categories = Object.keys(ROW_KIND_FOR_CATEGORY) as Array<MessageCategory['kind']>
    expect(
      categories.filter(kind => !covered.has(kind)),
      'Add a frame to ROW_KIND_CASES that classifies as this category. A category no case '
      + 'reaches is a band the virtual list measures and nothing checks the transcript draws.',
    ).toEqual([])
  })

  it.each(ROW_KIND_CASES)('$provider $name', ({ provider, payload, category, spanType, messageMetadata }) => {
    // A plugin for every case but the one whose whole subject is that it has none. Stated
    // both ways, so a rename that dropped a provider from the registry cannot take its
    // cases with it and leave the remaining assertions passing.
    expect(
      Boolean(providerFor(provider)),
      'every case but `unsupported_provider` states a registered provider',
    ).toBe(category !== 'unsupported_provider')
    const parsed = {
      ...input(payload, undefined, provider),
      ...(spanType !== undefined ? { spanType } : {}),
      ...(messageMetadata !== undefined ? { messageMetadata } : {}),
    }

    // Half one: the frame really is the shape the corpus claims. Without this the
    // pair below would agree on `unknown`/`unrecognized` for a shape no provider
    // sends, and the case would pass while proving nothing.
    //
    // `classifyMessage`, not `plugin?.transcript.classify`: it is the ONE function the chat view
    // calls, and it answers several categories before it asks a plugin at all -- among
    // them the saved control answer, the worker-written notification and the frame of a
    // provider with no plugin. A corpus that asked the plugin could reach none of them.
    const classified = classifyMessage(parsed)
    expect(classified.kind).toBe(category)

    // Half two: the row the transcript draws is the kind the list measured.
    const extraction = extractChatRow(provider, parsed, classified, { ...(spanType !== undefined ? { spanType } : {}) })
    expect(drawnRowKind(category, extraction)).toBe(ROW_KIND_FOR_CATEGORY[category])
  })

  // A corpus of one provider would satisfy every assertion above and still let the
  // next provider's two layers disagree. The pluginless case is excluded, because it
  // states no runtime at all.
  it('covers more than half the registered providers', () => {
    const covered = new Set(ROW_KIND_CASES.map(entry => entry.provider).filter(provider => providerFor(provider)))
    expect(covered.size).toBeGreaterThanOrEqual(6)
  })
})
