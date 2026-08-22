// @refresh reload
import { mount, StartClient } from '@solidjs/start/client'
import { showWarnToast } from '~/components/common/Toast'
import { installIgnorableErrorSuppressor } from '~/lib/ignorableErrorEvents'
import { installGlobalErrorSink } from '~/lib/installGlobalErrorSink'
import { scheduleRenderPipelineWarmup } from '~/lib/renderPipelineWarmup'

// Suppress the window errors that carry nothing to act on -- the benign
// "ResizeObserver loop ..." warning and an error the browser muted to "Script
// error." -- before mount(), so this listener is registered ahead of
// @solidjs/start's dev overlay (which registers its own window `error` listener
// during mount) and can stopImmediatePropagation the event before the overlay
// pops a 500 dialog. The long/busy chat transcript trips the loop routinely, and
// iOS Safari mutes an error when the share sheet resizes and snapshots the page;
// see the helper for the full rationale. Dev-only: the overlay only exists in
// dev, and prod keeps the browser's native error reporting untouched.
if (import.meta.env.DEV)
  installIgnorableErrorSuppressor()

// Catch what the ErrorBoundaries structurally cannot: faults thrown from event
// handlers, promise rejections, timers and socket callbacks never touch the
// render graph, so before this they were invisible to the user and unlogged in
// prod. Installed AFTER the suppressor so its capture-phase listener still wins
// on the benign ResizeObserver events (the sink filters them too, for prod,
// where the suppressor is not installed at all).
//
// `showWarnToast` is passed in rather than imported by the sink: it both renders
// the toast and logs at warn level, so the sink deliberately does neither and
// the fault is reported exactly once.
installGlobalErrorSink({ report: showWarnToast })

// Vinxi's generated client handler probes this module for a default export even
// though the Solid client entry only needs the side-effectful mount call below.
// Exporting a no-op default keeps the bundler quiet and is safe to ignore.
export default function EntryClient(): null {
  return null
}

mount(() => <StartClient />, document.getElementById('app')!)

// Warm the render workers (WASM engine, first grammar, remark processor) and
// sweep the persisted render-artifact store once the browser is idle, so the
// first visible code block doesn't pay the cold-start bill.
scheduleRenderPipelineWarmup()
