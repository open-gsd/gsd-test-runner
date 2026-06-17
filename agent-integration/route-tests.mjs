// route-tests.mjs — Claude Code PreToolUse hook that intercepts local
// node --test / npm test invocations and routes them to the gsd-test submit
// front door so the agent never spawns an orphan-prone local node process.
//
// ADR-0021 §G, issue #60.
//
// Exported pure functions are tested independently by route-tests.test.mjs.
// The main() entrypoint reads a Claude Code PreToolUse hook payload from stdin
// and either emits a deny decision (matched) or exits 0 silently (no match).

import { pathToFileURL } from 'node:url';

// ---------------------------------------------------------------------------
// Patterns
// ---------------------------------------------------------------------------

/**
 * Strip leading environment-variable assignments and normalise whitespace.
 * e.g. "CI=1 NODE_ENV=test npm test" → "npm test"
 * @param {string} command
 * @returns {string}
 */
function stripEnvPrefix(command) {
  // Match zero-or-more KEY=value tokens followed by optional spaces.
  return command.replace(/^(?:[A-Z_][A-Z0-9_]*=\S*\s+)+/, '').replace(/\s+/g, ' ').trim();
}

/**
 * Patterns for node --test style invocations.
 * We match:
 *   node --test …              (any trailing args)
 *   node --test-… …            (--test-timeout, --test-force-exit, etc.)
 *   node:test runner invoked via node --loader / --import
 */
const NODE_TEST_RE = /^node\s+(?:--test(?:[=-]\S*)?|--(?:loader|import)\s+\S+\s+--test)/;

/**
 * Patterns for npm/npx test invocations we want to intercept:
 *   npm test
 *   npm t
 *   npm run test
 *   npx … (excluded intentionally — too broad)
 */
const NPM_TEST_RE = /^npm\s+(?:test|t|run\s+test)(?:\s|$)/;

// ---------------------------------------------------------------------------
// Pure exported API
// ---------------------------------------------------------------------------

/**
 * Returns true when a shell command string is a Node test invocation that
 * should be routed to gsd-test submit instead of being run locally.
 *
 * Conservative: does NOT match `node build.js`, `npm run lint`, `npm install`,
 * `eslint`, or any unrelated npm lifecycle script.
 *
 * @param {string} command
 * @returns {boolean}
 */
export function isTestCommand(command) {
  if (typeof command !== 'string' || command.trim() === '') return false;
  const bare = stripEnvPrefix(command);
  return NODE_TEST_RE.test(bare) || NPM_TEST_RE.test(bare);
}

/**
 * Returns the replacement command the agent should use instead of running
 * node --test locally. The agent is expected to pipe a run spec on stdin.
 *
 * @param {string} _command  The original command (unused; kept for API symmetry).
 * @returns {string}
 */
export function rewrite(_command) {
  // The explicit executor (issue #67, ADR-0022): a named command that runs the
  // suite in Docker and prints a node:test verdict, so the agent just swaps
  // `node --test` → `gsd-test run` rather than hand-crafting a run spec.
  return 'gsd-test run';
}

// ---------------------------------------------------------------------------
// Hook entrypoint
// ---------------------------------------------------------------------------

/**
 * Read a Claude Code PreToolUse hook JSON payload from stdin, and if the
 * Bash tool's command is a node test invocation, write a deny decision to
 * stdout. If the command does not match, output nothing (exit 0).
 *
 * Deny shape (Claude Code PreToolUse hook protocol):
 *   {
 *     "hookSpecificOutput": {
 *       "hookEventName": "PreToolUse",
 *       "permissionDecision": "deny",
 *       "permissionDecisionReason": "<msg>"
 *     }
 *   }
 */
export async function main() {
  const chunks = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk);
  }
  const raw = Buffer.concat(chunks).toString('utf8').trim();
  if (!raw) return;

  let payload;
  try {
    payload = JSON.parse(raw);
  } catch {
    // Unparseable input — do nothing; let the hook pass through.
    return;
  }

  if (payload.tool_name !== 'Bash') return;
  const command = payload.tool_input && payload.tool_input.command;
  if (typeof command !== 'string') return;
  if (!isTestCommand(command)) return;

  const reason =
    `Local \`node --test\` / \`npm test\` invocations are intercepted by the ` +
    `gsd-test agent hook (ADR-0022, issue #60/#65). Running node --test directly ` +
    `on the Dev Workstation creates orphaned node processes that outlive the agent ` +
    `turn and can wedge the machine. Run your tests safely in Docker instead — ` +
    `it returns the same node:test verdict (and a loud, attributed failure if a ` +
    `test runs away):\n\n` +
    `  ${rewrite(command)}\n\n` +
    `Pass test path patterns as args (e.g. \`gsd-test run src/foo.test.mjs\`); ` +
    `\`--target\` selects the OS. See agent-integration/README.md and the ` +
    `run-and-die skill.`;

  const decision = {
    hookSpecificOutput: {
      hookEventName: 'PreToolUse',
      permissionDecision: 'deny',
      permissionDecisionReason: reason,
    },
  };

  process.stdout.write(JSON.stringify(decision) + '\n');
}

// Run as a CLI when invoked directly, not when imported.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err) => {
    process.stderr.write(`route-tests: ${err.message}\n`);
    process.exit(1);
  });
}
