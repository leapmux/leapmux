// @refresh reload
import { mount, StartClient } from '@solidjs/start/client'
import { installResizeObserverLoopErrorSuppressor } from '~/lib/suppressResizeObserverLoopError'

// Suppress the benign "ResizeObserver loop ..." window error before mount(), so
// this listener is registered ahead of @solidjs/start's dev overlay (which
// registers its own window `error` listener during mount) and can
// stopImmediatePropagation the event before the overlay pops a 500 dialog. The
// long/busy chat transcript trips this loop routinely; see the helper for the
// full rationale. Dev-only: the overlay only exists in dev, and prod keeps the
// browser's native error reporting untouched.
if (import.meta.env.DEV)
  installResizeObserverLoopErrorSuppressor()

// Vinxi's generated client handler probes this module for a default export even
// though the Solid client entry only needs the side-effectful mount call below.
// Exporting a no-op default keeps the bundler quiet and is safe to ignore.
export default function EntryClient(): null {
  return null
}

mount(() => <StartClient />, document.getElementById('app')!)
