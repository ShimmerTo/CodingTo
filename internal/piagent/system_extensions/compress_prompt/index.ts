import type { ExtensionAPI } from '@earendil-works/pi-coding-agent';
import { readFileSync } from 'node:fs';
import path from 'node:path';

// Re-entrancy guard: when we re-trigger compaction with the appended
// instructions, the hook fires again. On that second pass we clear the flag and
// let Pi's built-in summarizer run with the augmented instructions.
let augmenting = false;

export default function codingToCompressPrompt(api: ExtensionAPI) {
  api.on('session_before_compact', async (event: any, ctx: any) => {
    // Second pass: our re-triggered compaction carries the augmented
    // instructions. Allow Pi's default summarizer to use them.
    if (augmenting) {
      augmenting = false;
      return {};
    }

    const dataDir = process.env.PI_CODING_AGENT_DIR;
    if (!dataDir) return {};

    let extra = '';
    try {
      extra = readFileSync(path.join(dataDir, 'PROMPT_COMPRESS.md'), 'utf8').trim();
    } catch {
      // No custom compression prompt configured; use Pi's built-in behavior.
      return {};
    }
    if (!extra) return {};

    const base = (event?.customInstructions || '').trim();
    const combined = (base ? base + '\n\n' : '') + extra;

    augmenting = true;
    try {
      ctx.compact({
        customInstructions: combined,
        onComplete: () => {
          augmenting = false;
        },
        onError: (e: any) => {
          augmenting = false;
          ctx.ui?.notify?.(`Compaction failed: ${e?.message ?? e}`, 'error');
        },
      });
    } catch {
      augmenting = false;
      return {};
    }
    // Cancel the original (un-augmented) compaction; the re-triggered one runs
    // with PROMPT_COMPRESS.md appended after Pi's built-in compaction prompt.
    return { cancel: true };
  });
}
