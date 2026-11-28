# Review JSON Schemas (`--output-schema`)

Save one of the schemas below under `.leapmux/tmp/` (for example `.leapmux/tmp/review-schema.json`) and pass it with a prompt file to `leapmux agent open --wait --output-schema .leapmux/tmp/review-schema.json --file .leapmux/tmp/review-brief.md` (`--output-schema` requires `--message` or `--file`).

## Code Diff Review Schema

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "verdict": { "type": "string", "enum": ["approve", "changes_requested"] },
    "summary": { "type": "string" },
    "checked_areas": { "type": "array", "items": { "type": "string" } },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "severity": { "type": "string", "enum": ["high", "medium", "low"] },
          "location": { "type": "string" },
          "issue": { "type": "string" },
          "recommendation": { "type": "string" }
        },
        "required": ["severity", "location", "issue"]
      }
    }
  },
  "required": ["verdict", "findings"]
}
```

## Architecture / Plan Review Schema

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "verdict": { "type": "string", "enum": ["ready", "revise", "rethink"] },
    "strongest_points": { "type": "array", "items": { "type": "string" } },
    "risks": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "severity": { "type": "string", "enum": ["high", "medium", "low"] },
          "topic": { "type": "string" },
          "concern": { "type": "string" },
          "alternative": { "type": "string" }
        },
        "required": ["severity", "topic", "concern"]
      }
    },
    "missing_prerequisites": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["verdict", "risks"]
}
```

## Security Audit Schema

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "risk_level": { "type": "string", "enum": ["none", "low", "medium", "high", "critical"] },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "severity": { "type": "string", "enum": ["critical", "high", "medium", "low"] },
          "category": { "type": "string" },
          "location": { "type": "string" },
          "exploit_scenario": { "type": "string" },
          "fix": { "type": "string" }
        },
        "required": ["severity", "category", "location", "exploit_scenario", "fix"]
      }
    }
  },
  "required": ["risk_level", "findings"]
}
```
