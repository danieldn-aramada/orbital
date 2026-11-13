// Rebuilds web/shared/static/graphiql-<version>/ from the versions pinned in
// package-lock.json, so GraphiQL loads with no network access.
import * as esbuild from 'esbuild';
import fs from 'node:fs';
import path from 'node:path';

const root = path.resolve(import.meta.dirname, '..');
const staticDir = path.join(root, 'web/shared/static');
const { version } = readJSON(path.join(root, 'node_modules/graphiql/package.json'));
const outdir = path.join(staticDir, `graphiql-${version}`);

// Two copies of any of these break GraphiQL without an error at build time.
const singletons = ['graphql', 'monaco-editor', 'react', 'react-dom'];
const allowedLicences = new Set(['MIT', 'ISC', 'Apache-2.0', 'BSD-2-Clause', 'BSD-3-Clause', '0BSD', 'BlueOak-1.0.0']);

for (const dir of fs.readdirSync(staticDir)) {
  if (/^graphiql-\d/.test(dir)) fs.rmSync(path.join(staticDir, dir), { recursive: true });
}

const common = {
  absWorkingDir: root,
  bundle: true,
  minify: true,
  target: 'es2022',
  legalComments: 'none',
  metafile: true,
  logLevel: 'warning',
  outdir,
  define: { 'process.env.NODE_ENV': '"production"' },
};

const page = await esbuild.build({
  ...common,
  entryPoints: { graphiql: 'web/graphiql/index.js' },
  format: 'esm',
  loader: { '.ttf': 'file' },
  assetNames: '[name]',
});

const workers = await esbuild.build({
  ...common,
  entryPoints: {
    'editor.worker': 'node_modules/monaco-editor/esm/vs/editor/editor.worker.js',
    'json.worker': 'node_modules/monaco-editor/esm/vs/languages/features/json/json.worker.js',
    'graphql.worker': 'node_modules/monaco-graphql/esm/graphql.worker.js',
  },
  format: 'iife',
});

const packages = new Set(
  [page, workers].flatMap((r) => Object.keys(r.metafile.inputs)).map(packageDir).filter(Boolean),
);

for (const name of singletons) {
  const copies = [...packages].filter((dir) => dir.endsWith(`node_modules/${name}`));
  if (copies.length > 1) fail(`${name} is bundled from ${copies.length} copies: ${copies.join(', ')}`);
}

const notices = [...packages].sort().map((dir) => {
  const pkg = readJSON(path.join(root, dir, 'package.json'));
  if (!licenceAllowed(pkg.license)) fail(`${pkg.name}@${pkg.version}: licence ${JSON.stringify(pkg.license)} is not in the allowed set`);
  const texts = fs
    .readdirSync(path.join(root, dir))
    .filter((f) => /^(licen[cs]e|notice|thirdpartynotices)/i.test(f))
    .sort()
    .map((f) => fs.readFileSync(path.join(root, dir, f), 'utf8').trim());
  return [`${pkg.name}@${pkg.version} (${pkg.license})`, ...texts].join('\n\n');
});
fs.writeFileSync(path.join(outdir, 'THIRD_PARTY_LICENSES.txt'), notices.join(`\n\n${'='.repeat(80)}\n\n`) + '\n');

const htmlPath = path.join(staticDir, 'graphiql.html');
const html = fs.readFileSync(htmlPath, 'utf8');
fs.writeFileSync(htmlPath, html.replace(/graphiql-\d[^/"]*\//g, `graphiql-${version}/`));

console.log(`graphiql ${version}: ${packages.size} packages bundled into ${path.relative(root, outdir)}/`);

// packageDir returns the node_modules/<name> directory an input file belongs to, or "" for our own sources.
function packageDir(input) {
  const m = input.match(/^(.*node_modules\/(?:@[^/]+\/)?[^/]+)\//);
  return m ? m[1] : '';
}

// licenceAllowed accepts an SPDX expression if every AND term, or any OR term, is allowed.
function licenceAllowed(expr) {
  if (typeof expr !== 'string') return false;
  const e = expr.replace(/[()]/g, '').trim();
  if (e.includes(' OR ')) return e.split(' OR ').some((t) => allowedLicences.has(t.trim()));
  return e.split(' AND ').every((t) => allowedLicences.has(t.trim()));
}

function readJSON(p) {
  return JSON.parse(fs.readFileSync(p, 'utf8'));
}

function fail(msg) {
  console.error(`vendor-graphiql: ${msg}`);
  process.exit(1);
}
