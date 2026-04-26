// Side-effect-only barrel: each import here exists to load the corresponding
// renderer module so its `defineCodexRenderer({...})` call registers into
// `CODEX_RENDERERS`. The plugin's `renderMessage` looks up renderers by
// `item.type` from that registry; for renderers also dispatched by name
// (turnPlan, turnCompleted, reasoning, agentMessage, mcp fallback), the
// plugin imports them directly from `./` — those imports also load the
// modules, but the registry-only renderers (commandExecution, fileChange,
// plan, webSearch, collabAgentToolCall) need this barrel to ensure they
// aren't tree-shaken out of the bundle.

import './agentMessage'
import './collabAgentToolCall'
import './commandExecution'
import './fileChange'
import './mcpToolCall'
import './plan'
import './reasoning'
import './webSearch'
