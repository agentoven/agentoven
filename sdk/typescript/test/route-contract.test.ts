import { readdirSync, readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

/**
 * Every call the TypeScript client makes names a (verb, path) the server has.
 *
 * sdk/fixtures/server-routes.json is the server's route list, generated from its chi tree (agentoven-pro's
 * internal/routeguard route test, with UPDATE_SDK_ROUTES=1). This reads the client's source for each call and
 * fails for one the server does not serve: a PUT where the server wants PATCH, a path that was never a
 * route, a method for a route that was removed. It reads the call sites, so write the path as a literal at the
 * call (a template with ${} parameters is fine; a path held in a variable is not seen).
 */
const SRC = new URL('../src/', import.meta.url);
const FIXTURE = new URL('../../fixtures/server-routes.json', import.meta.url);

/** Replace each ${...} (braces may nest: `${queryString({ a })}`) with {}; an unclosed one is dropped with the rest. */
function placeholders(text: string): string {
  let out = '';
  for (let i = 0; i < text.length; i++) {
    if (text[i] === '$' && text[i + 1] === '{') {
      let depth = 0;
      let j = i + 1;
      for (; j < text.length; j++) {
        if (text[j] === '{') depth++;
        else if (text[j] === '}' && --depth === 0) break;
      }
      if (j >= text.length) break; // unclosed: a quote nested in the expression ended the literal early
      out += '{}';
      i = j;
    } else {
      out += text[i];
    }
  }
  return out;
}

function normalize(path: string): string {
  let p = placeholders(path).replace(/\{[^}]*\}/g, '{}');
  p = p.split('?')[0];
  p = p.replace(/(?<=[^/])\{\}$/, ''); // "/api/v1/traces${qs}": the placeholder was a query string
  return p.replace(/\/+$/, '') || '/';
}

const SHORTHAND = /this\.(get|post|put|delete|patch)(?:<[^>]*>)?\(\s*[`'"](\/[^`'"]*)/g;
const EXPLICIT = /(?:this\.)?(?:request|requestText|send)(?:<[^>]*>)?\(\s*['"](GET|POST|PUT|DELETE|PATCH)['"]\s*,\s*[`'"](\/[^`'"]*)/g;

function clientCalls(): { file: string; line: number; verb: string; path: string }[] {
  const out: { file: string; line: number; verb: string; path: string }[] = [];
  for (const file of readdirSync(SRC).filter((f) => f.endsWith('.ts') && !f.endsWith('.d.ts'))) {
    const text = readFileSync(new URL(file, SRC), 'utf8');
    for (const rx of [SHORTHAND, EXPLICIT]) {
      for (const m of text.matchAll(rx)) {
        const line = text.slice(0, m.index).split('\n').length;
        out.push({ file, line, verb: m[1].toUpperCase(), path: m[2] });
      }
    }
  }
  return out;
}

function serverRoutes(): Set<string> {
  const data = JSON.parse(readFileSync(FIXTURE, 'utf8')) as { routes: { method: string; path: string }[] };
  return new Set(data.routes.map((r) => `${r.method} ${normalize(r.path)}`));
}

describe('route contract', () => {
  it('the scanner sees the client calls', () => {
    // A scanner that matched nothing would pass every contract: hold it to the size of what it must see.
    expect(clientCalls().length).toBeGreaterThan(150);
  });

  it('the configuration checks are among the calls it sees, and the server has them', () => {
    const seen = new Set(clientCalls().map((c) => `${c.verb} ${normalize(c.path)}`));
    const routes = serverRoutes();
    for (const call of [
      'GET /api/v1/agents/{}/validate',
      'GET /api/v1/recipes/{}/validate',
      'POST /api/v1/agents',
      'PUT /api/v1/agents/{}',
      'POST /api/v1/agents/{}/bake',
      'POST /api/v1/agents/{}/recook',
      'GET /api/v1/skills',
      'GET /api/v1/prompts',
    ]) {
      expect(seen.has(call), `the client makes no ${call}`).toBe(true);
      expect(routes.has(call), `the server has no ${call}`).toBe(true);
    }
  });

  it('every call the client makes is a route the server has', () => {
    const routes = serverRoutes();
    const missing = clientCalls()
      .filter((c) => !routes.has(`${c.verb} ${normalize(c.path)}`))
      .map((c) => `${c.file}:${c.line} ${c.verb} ${c.path}`);
    expect(missing, `calls the server has no route for:\n  ${missing.join('\n  ')}`).toEqual([]);
  });
});
