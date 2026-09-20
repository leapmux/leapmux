import type { ClassifiedEntry } from '~/components/chat/chatEntryCache'
import type { MessageContextResolver, MessageContextSources } from '~/components/chat/messageContextResolver'
import type { ToolCallRow } from '~/components/chat/model/row'
import type { ChatRowExtraction } from '~/components/chat/rowExtraction'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TranscriptFrame } from '~/test-support/messageFactory'
import { render } from '@solidjs/testing-library'
import { createRoot } from 'solid-js'
import { createClassifiedEntryCache, renderKeyForEntry } from '~/components/chat/chatEntryCache'
import { MessageBubble } from '~/components/chat/MessageBubble'
import { createMessageRenderSources } from '~/components/chat/messageContextResolver'
import { createMessageRenderCacheStore } from '~/components/chat/messageRenderCache'
import { cachedChatRow } from '~/components/chat/rowModelCache'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { messageSpanIdentity } from '~/lib/messageSpan'
import { cleanups, testTranscriptContext } from '~/test-support/messageContext'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
// Every provider plugin registers at import time; the scenario reads them all.
import '~/components/chat/providers'
import '../components/chat/providers/registry'

// One provider transcript, exercised end to end in milliseconds: the real parse,
// the real resolution, the real classification, the real span index and pairing,
// the real per-revision caches, and the real `MessageBubble`.
//
// The E2E suite proved these paths against a browser, a worker and a database;
// this harness proves the same paths against fixtures, so a provider frame walks
// the pipeline the app walks and a test asserts on what the app would draw. What
// it does NOT model stays in the E2E suite: the store's SQL window, the worker's
// transport, and the browser's own clipboard and image codecs.

/** What a scenario overrides on the transport sources. */
export interface TranscriptScenarioOptions {
  /** The complete transcript: the loaded window and every fetch read from it. */
  archive: readonly AgentChatMessage[]
  /** The ids the loaded window starts from; every archive id when omitted. */
  windowIds?: readonly string[]
  fetchSpan?: MessageContextSources['fetchSpan']
  fetchMessage?: MessageContextSources['fetchMessage']
}

export interface TranscriptScenario {
  /** The resolver the whole scenario reads through. */
  resolver: MessageContextResolver
  /** The loaded window, in display order. */
  messages: () => AgentChatMessage[]
  /** The classified entry for one message id, from the entry cache the transcript uses. */
  entry: (id: string) => ClassifiedEntry
  /** The row model for one message id, through the render sources and the row cache. */
  extract: (id: string) => ChatRowExtraction
  /** The tool row for one message id: `extract`, narrowed and checked. */
  toolRow: (id: string) => ToolCallRow
  /** The row for one message id, drawn by the real `MessageBubble` in jsdom. */
  renderBubble: (id: string) => ReturnType<typeof render>
  /** Load the span of one message id through the resolver's fetch path. */
  loadSpan: (id: string) => Promise<void>
  /** A frame of a NEW id: it enters the archive and the window. */
  append: (frame: TranscriptFrame) => void
  /** A frame of an id the archive holds: an in-place replacement. */
  replace: (frame: TranscriptFrame) => void
  /** A live arrival: the replacement, plus the observer broadcast. */
  broadcast: (frame: TranscriptFrame) => void
  /** Swap the loaded window; the archive stays whole for the fetches. */
  replaceWindow: (ids: readonly string[]) => void
}

export function createTranscriptScenario(options: TranscriptScenarioOptions): TranscriptScenario {
  // The whole scenario -- the transcript, the entry cache and every memo they
  // hold -- lives in one root that the test-support cleanup disposes, so a
  // scenario built outside a component test leaves nothing behind.
  return createRoot((dispose) => {
    cleanups.push(dispose)
    return buildTranscriptScenario(options)
  })
}

function buildTranscriptScenario(options: TranscriptScenarioOptions): TranscriptScenario {
  const { transcript, resolver } = testTranscriptContext(
    options.archive,
    {
      ...(options.fetchSpan !== undefined ? { fetchSpan: options.fetchSpan } : {}),
      ...(options.fetchMessage !== undefined ? { fetchMessage: options.fetchMessage } : {}),
    },
  )
  if (options.windowIds !== undefined)
    transcript.replaceWindow(options.windowIds)

  const renderCacheStore = createMessageRenderCacheStore()
  const entries = createClassifiedEntryCache({
    messages: () => transcript.sources.messages(),
    requestRevision: identity => resolver.request(identity)?.revision,
    resultRevision: identity => resolver.result(identity)?.revision,
    contentVersionById: id => resolver.contentVersion(id),
    resolvedMessage: message => resolver.resolvedMessage(message),
    role: message => resolver.role(message),
    showHiddenMessages: () => false,
  })

  let nextSeq = options.archive.reduce((max, message) => (message.seq > max ? message.seq : max), 0n) + 1n

  function entry(id: string): ClassifiedEntry {
    const found = entries.visibleEntries().find(candidate => candidate.message.id === id)
    if (found === undefined) {
      throw new Error(`No classified entry for "${id}". The window holds: ${entries.visibleEntries().map(candidate => candidate.message.id).join(', ') || 'nothing'}.`)
    }
    return found
  }

  function messageOf(id: string): AgentChatMessage {
    return entry(id).message
  }

  function extract(id: string): ChatRowExtraction {
    const prepared = entry(id)
    const message = prepared.message
    const sources = createMessageRenderSources(() => resolver, () => message, () => prepared.resolved)
    return cachedChatRow(
      { renderCache: renderCacheStore.forRow(renderKeyForEntry(prepared)), sources, spanType: message.spanType },
      message.agentProvider,
      prepared.resolved,
      prepared.category,
      message.completion,
    )
  }

  function toolRow(id: string): ToolCallRow {
    const extraction = extract(id)
    if (extraction.kind !== 'row') {
      throw new Error(`Message "${id}" extracted as ${extraction.kind}, not a row.`)
    }
    const row = extraction.row
    if (row.kind !== 'tool') {
      throw new Error(`Message "${id}" extracted a "${row.kind}" row, not a tool row.`)
    }
    return row
  }

  function renderBubble(id: string): ReturnType<typeof render> {
    const prepared = entry(id)
    const message = prepared.message
    return render(() => (
      <PreferencesProvider>
        <MessageBubble
          message={message}
          prepared={prepared}
          host={{
            messages: resolver,
            get renderCache() {
              return renderCacheStore.forRow(renderKeyForEntry(prepared))
            },
          }}
        />
      </PreferencesProvider>
    ))
  }

  function appendFrame(frame: TranscriptFrame): void {
    transcript.append(makeTranscriptMessage(frame, nextSeq))
    nextSeq += 1n
  }

  // A replacement keeps the resident message's sequence unless the frame states
  // one: it is the store's in-place merge, not a new arrival.
  function replaceFrame(frame: TranscriptFrame, broadcast: boolean): void {
    const resident = [...transcript.sources.messages()].find(message => message.id === frame.id)
    if (resident === undefined)
      throw new Error(`No message named "${frame.id}" to replace. The window holds: ${transcript.windowIds().join(', ') || 'nothing'}.`)
    const message = makeTranscriptMessage(frame, frame.seq ?? resident.seq)
    if (broadcast)
      transcript.broadcast(message)
    else
      transcript.replace(message)
  }

  return {
    resolver,
    messages: () => [...transcript.sources.messages()],
    entry,
    extract,
    toolRow,
    renderBubble,
    loadSpan: async (id) => {
      await resolver.loadSpan(messageSpanIdentity(messageOf(id)))
    },
    append: appendFrame,
    replace: frame => replaceFrame(frame, false),
    broadcast: frame => replaceFrame(frame, true),
    replaceWindow: transcript.replaceWindow,
  }
}
