// Import all provider modules to trigger side-effect registrations.
import './claude'
import './copilot'
import './codex'
import './gemini'
import './opencode'
import './stubs'

export { getProviderPlugin, registerProvider } from './registry'
export type { ProviderPlugin } from './registry'
