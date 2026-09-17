// Side-effect imports: each plugin module calls registerProvider() at import time.
// Removing one silently breaks that provider's providerFor() lookup in registry.ts.
import './claude/plugin'
import './codex/plugin'
import './opencode/plugin'
import './pi/plugin'
import './copilot/plugin'
import './cursor/plugin'
import './goose/plugin'
import './kilo/plugin'
import './reasonix/plugin'
import './zcode/plugin'
