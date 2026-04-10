// @refresh reload
import { mount, StartClient } from '@solidjs/start/client'

// Vinxi's generated client handler probes this module for a default export even
// though the Solid client entry only needs the side-effectful mount call below.
// Exporting a no-op default keeps the bundler quiet and is safe to ignore.
export default function EntryClient(): null {
  return null
}

mount(() => <StartClient />, document.getElementById('app')!)
