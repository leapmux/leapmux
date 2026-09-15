// Side-effect imports: each provider module calls registerProvider() at import time.
// Removing or reordering these silently breaks providerFor() lookups in registry.ts.
import './claude'
import './codex'
import './opencode'
import './pi'
import './copilot'
import './cursor'
import './goose'
import './kilo'
import './reasonix'
import './zcode'
