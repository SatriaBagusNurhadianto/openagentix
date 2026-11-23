// @ts-check
const config = {
  title: 'OpenAgentix',
  tagline: 'Kubernetes-native platform for running AI agents',
  favicon: 'img/favicon.svg',
  url: 'https://openagentix.github.io',
  baseUrl: '/openagentix/',
  organizationName: 'openagentix',
  projectName: 'openagentix',
  trailingSlash: false,
  onBrokenLinks: 'throw',
  i18n: {defaultLocale: 'en', locales: ['en']},
  markdown: {
    format: 'detect',
    mermaid: true,
    hooks: {onBrokenMarkdownLinks: 'throw'},
  },
  themes: ['@docusaurus/theme-mermaid'],
  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs/diary',
          routeBasePath: '/',
          exclude: ['**/README.md'],
          sidebarPath: './sidebars.js',
          editUrl: 'https://github.com/openagentix/openagentix/tree/main/docs/diary/',
        },
        blog: false,
        theme: {},
      },
    ],
  ],
  plugins: [
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'decisions',
        path: '../docs/decisions',
        routeBasePath: 'decisions',
        sidebarPath: './sidebars-decisions.js',
        editUrl: 'https://github.com/openagentix/openagentix/tree/main/docs/decisions/',
      },
    ],
  ],
  themeConfig: {
    colorMode: {defaultMode: 'light', respectPrefersColorScheme: true},
    navbar: {
      title: 'OpenAgentix',
      items: [
        {to: '/', label: 'Diary', position: 'left', activeBaseRegex: '^/openagentix/(?!decisions)'},
        {type: 'docSidebar', sidebarId: 'decisions', docsPluginId: 'decisions', label: 'Decisions', position: 'left'},
        {href: 'https://github.com/openagentix/openagentix', label: 'GitHub', position: 'right'},
      ],
    },
    footer: {style: 'dark', copyright: 'Apache-2.0'},
  },
};
module.exports = config;
