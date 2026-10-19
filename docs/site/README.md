# Orbital docs site

Docusaurus. Content lives in `content/` (not `docs/`), and the sidebar is generated from its folders.

```bash
npm install
npm run start   # live-reloading dev server
npm run build   # fails on any broken link or anchor
```

## Vocabulary

`content/concepts/glossary.md` defines every term the site uses. Use them in those meanings; add a
term there before using a new one.

## Examples and screenshots

Every example and screenshot uses the `docs-example` data center in `examples/docs/`, which
`make seed` always loads. Never show a real deployment's data.

Screenshots are generated, not captured by hand. From the repo root, with the stack up and seeded:

```bash
ORBITAL_VIEWS_OVERLAY_PATH=examples/docs/views-overlay.yaml make run-orbital
make docs-screenshots   # writes static/img/ui/*.png
```

The overlay adds the Racks page that *Defining Pages → Add a page* walks through. A spec in
`screenshots/` that fails means the UI moved under it — update the shot and the page that describes it.
