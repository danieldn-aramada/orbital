import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import {themes as prismThemes} from 'prism-react-renderer';

const GH = 'https://github.com/danieldn-aramada/orbital';

const config: Config = {
  title: 'Orbital',
  tagline: 'Source of truth for edge, air-gapped modular data centers',
  favicon: 'img/favicon.svg',
  // Driven by env so the pending GitHub org migration is a deploy-time change,
  // not a code change.
  url: process.env.DOCS_URL ?? 'https://example.com',
  baseUrl: process.env.DOCS_BASE_URL ?? '/',
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  markdown: {mermaid: true, hooks: {onBrokenMarkdownLinks: 'warn'}},
  themes: [
    '@docusaurus/theme-mermaid',
    // Build-time index: no third-party service, and it works in an offline copy
    // of the docs — which matters for a product whose premise is air-gap.
    // Algolia DocSearch needs a live public domain, so it cannot be set up yet.
    ['@easyops-cn/docusaurus-search-local',
      {hashed: true, indexBlog: false, docsRouteBasePath: '/'}],
  ],
  presets: [
    ['classic', {
      docs: {
        // content/, not docs/ — this site lives INSIDE the repo's docs/ tree,
        // and a nested docs/site/docs/ would collide with CLAUDE.md's
        // "grep docs/ to find the right reference doc" instruction.
        path: 'content',
        routeBasePath: '/',
        sidebarPath: './sidebars.ts',
        editUrl: `${GH}/edit/main/docs/site/content/`,
      },
      blog: false,
      theme: {customCss: './src/css/custom.css'},
    } satisfies Preset.Options],
  ],
  themeConfig: {
    colorMode: {defaultMode: 'light', respectPrefersColorScheme: true},
    mermaid: {theme: {light: 'neutral', dark: 'dark'}},
    docs: {sidebar: {hideable: true, autoCollapseCategories: false}},
    navbar: {
      title: 'Orbital',
      items: [
        {type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Documentation'},
        {href: GH, position: 'right', className: 'header-github-link', 'aria-label': 'GitHub repository'},
      ],
    },
    prism: {
      // Explicit pairing. Dropped when this config was rewritten wholesale,
      // which left light-mode text (#1c1e21) on the dark code background
      // (#292d3e) — about 1.2:1, unreadable.
      // Chosen on token coverage, measured across prism-react-renderer's themes.
      // The scaffold pairing (github/dracula) styles 9 and 6 token types for 7
      // and 6 distinct colours — dracula is the weakest dark theme available, so
      // GraphQL keywords, class-names and punctuation collapsed into near-identical
      // greys. jettwave covers 9 types with 8 and 9 colours, and both sides carry a
      // tinted background so a code block is distinguishable from the page.
      theme: prismThemes.jettwaveLight,
      darkTheme: prismThemes.jettwaveDark,
      // Go, GraphQL, YAML and JSON ship in prism-react-renderer's default
      // bundle; bash and diff do not, and every sampled site adds bash.
      additionalLanguages: ['bash', 'diff'],
    },
    footer: {
      style: 'light',
      copyright: `Orbital is built and maintained by Armada. © ${new Date().getFullYear()}`,
    },
  } satisfies Preset.ThemeConfig,
};
export default config;
