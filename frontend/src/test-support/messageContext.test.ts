import { describe, expect, it } from 'vitest'
import { testTranscriptContext } from './messageContext'
import { makeMessage } from './messageFactory'

const archive = [
  makeMessage({ id: 'first', seq: 1n }),
  makeMessage({ id: 'second', seq: 2n }),
  makeMessage({ id: 'third', seq: 3n }),
]

describe('createMutableTranscript', () => {
  it('holds the live tail with the whole archive in the window', () => {
    const { transcript } = testTranscriptContext(archive)

    expect(transcript.sources.holdsLiveTail()).toBe(true)
  })

  it('holds the live tail with a window that lacks only older messages', () => {
    const { transcript } = testTranscriptContext(archive)

    transcript.replaceWindow(['second', 'third'])

    expect(transcript.sources.holdsLiveTail()).toBe(true)
  })

  it.each([
    [
      'the newest message',
      ['first', 'second'],
    ],
    [
      'a message between two that it holds',
      ['first', 'third'],
    ],
  ])('does not hold the live tail with a window that lacks %s', (_lacked, ids) => {
    const { transcript } = testTranscriptContext(archive)

    transcript.replaceWindow(ids)

    expect(transcript.sources.holdsLiveTail()).toBe(false)
  })

  it('holds the live tail with an empty window', () => {
    const { transcript } = testTranscriptContext(archive)

    transcript.replaceWindow([])

    expect(transcript.sources.holdsLiveTail()).toBe(true)
  })

  it('holds the live tail again once an appended message joins the window', () => {
    const { transcript } = testTranscriptContext(archive)
    transcript.replaceWindow(['first', 'second'])

    transcript.replaceWindow(['first', 'second', 'third'])
    transcript.append(makeMessage({ id: 'fourth', seq: 4n }))

    expect(transcript.sources.holdsLiveTail()).toBe(true)
  })
})
