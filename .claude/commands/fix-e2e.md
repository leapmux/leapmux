---
description: Run all e2e tests, fix failures, and ensure each passes 3 times consecutively
allowed-tools:
  - Bash
  - Read
  - Write
  - Edit
  - Glob
  - Grep
  - Agent
  - TodoWrite
---

# Fix E2E Tests

You are fixing all e2e tests in the project. Each test must pass three consecutive runs before moving on. After all individual tests pass, the full suite must pass as a final validation.

**IMPORTANT**: These instructions MUST be followed exactly, even after context compaction.

## Step 1: Discover Test Files

Find all e2e test files:

```bash
find frontend/tests/e2e -name "*.spec.ts" | sort
```

## Step 2: Create To-Do List

Use the `TodoWrite` tool to create one to-do item per test file. Each item should include the file name so progress is easy to track. This list is your source of truth — update it as you go.

## Step 3: Process Each Test

Work through the to-do list one test at a time. For each test file:

### 3a. Run the Test

```bash
task test-e2e -- "frontend/tests/e2e/<test-file>.spec.ts" 2>&1 | tee e2e-test.log | tail -30
```

- Always run exactly **one** test file per invocation. Never batch multiple tests or run tests in parallel.
- Always pipe through `tee` and `tail` to capture full output while keeping the terminal concise.
- If the displayed output is insufficient, read `e2e-test.log` with the `Read` tool.

### 3b. If the Test Fails — Fix the Root Cause

1. **Analyze**: Read the full error output from `e2e-test.log`. Examine stack traces, error messages, screenshots, and Playwright context and trace files if available.
2. **Investigate**: Read the relevant source code to understand why the failure occurs. Do not guess — trace the issue to its root cause.
3. **Fix**: Apply a fix that addresses the underlying problem.
   - Do NOT apply workarounds, retry logic, or increased timeouts to mask flaky behavior.
   - Do NOT add `test.skip`, `test.fixme`, or conditional skips.
   - Do NOT re-run the test immediately hoping it was flaky — analyze first, fix first.
4. **Re-run**: Go back to step 3a and run the same test again.

### 3c. Confirm Stability — Three Consecutive Passes

The test must pass **three consecutive runs** before it is considered stable. Track the pass count:

- Run 1: Pass → continue to run 2
- Run 2: Pass → continue to run 3
- Run 3: Pass → mark the to-do item as completed, move to the next test

If any run fails, reset the count to zero, fix the issue, and start over from run 1.

## Step 4: Run the Full Suite

Once every individual test has been marked complete:

```bash
task test-e2e 2>&1 | tee e2e-test.log | tail -30
```

If the full suite reveals new failures (e.g., test interactions, ordering issues, resource contention):

1. Analyze and fix the root cause following the same principles from step 3b.
2. Re-run the full suite until it passes cleanly.
