// Side-effect imports: each provider module calls registerProvider() at import time.
// Removing or reordering these silently breaks providerFor() lookups in registry.ts.
import './claude'
import './codex'
import './opencode'
import './pi'
import './stubs/copilot'
import './stubs/cursor'
import './stubs/gemini'
import './stubs/goose'
import './stubs/kilo'
