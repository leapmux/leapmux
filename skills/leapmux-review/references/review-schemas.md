# Review JSON Schemas (`--output-schema`)

Save one of the schemas below to a file (for example `.tmp/review-schema.json`) and pass it to `leapmux agent open --wait --output-schema .tmp/review-schema.json`.

## Code Diff Review Schema

```json
{
  "type": "object",
  "properties": {
    "verdict": { "type": "string", "enum": ["approve", "changes_requested"] },
    "summary": { "type": "string" },
    "checked_areas": { "type": "array", "items": { "type": "string" } },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
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
  "properties": {
    "verdict": { "type": "string", "enum": ["ready", "revise", "rethink"] },
    "strongest_points": { "type": "array", "items": { "type": "string" } },
    "risks": {
      "type": "array",
      "items": {
        "type": "object",
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
  "properties": {
    "risk_level": { "type": "string", "enum": ["none", "low", "medium", "high", "critical"] },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
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
