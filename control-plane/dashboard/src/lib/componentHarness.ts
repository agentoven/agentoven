/// <reference types="node" />
// Renders a .tsx component to HTML inside a node:test, with no jsdom: the component is bundled with esbuild (the
// one vite ships) and rendered with react-dom/server. It sees the first render only, which is what the tests of
// the dropdowns need (the open list, the chips, the empty state); behaviour that depends on events lives in
// lib/references.ts and is tested there.
import { mkdirSync, unlinkSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { build } from 'esbuild';

const require = createRequire(import.meta.url);
const root = resolve(dirname(new URL(import.meta.url).pathname), '..', '..');

// Packages that must be the same copy in the test and in the bundle (one React, one router).
const SHARED = ['react', 'react-dom', 'react-router-dom', 'lucide-react'];

/** Bundles the module at `entry` (relative to the dashboard's src) and imports it. */
export async function loadModule<T>(entry: string): Promise<T> {
  const out = await build({
    entryPoints: [resolve(root, 'src', entry)],
    bundle: true,
    write: false,
    format: 'esm',
    platform: 'node',
    jsx: 'automatic',
    external: SHARED,
    logLevel: 'silent',
  });
  const dir = resolve(root, 'node_modules', '.cache', 'ao-component-tests');
  mkdirSync(dir, { recursive: true });
  const file = resolve(dir, `${entry.replace(/[^a-z0-9]+/gi, '_')}-${process.pid}.mjs`);
  writeFileSync(file, out.outputFiles[0].text);
  const mod = (await import(pathToFileURL(file).href)) as T;
  unlinkSync(file); // loaded; nothing needs the file again
  return mod;
}

/** The HTML of an element, with a router around it (the dropdowns link to the page that registers what is missing). */
export function render(element: unknown): string {
  const React = require('react') as typeof import('react');
  const { renderToStaticMarkup } = require('react-dom/server') as typeof import('react-dom/server');
  const { MemoryRouter } = require('react-router-dom') as typeof import('react-router-dom');
  return renderToStaticMarkup(React.createElement(MemoryRouter, null, element as import('react').ReactNode));
}

export const h = (type: unknown, props: Record<string, unknown> | null) => {
  const React = require('react') as typeof import('react');
  return React.createElement(type as string, props);
};
