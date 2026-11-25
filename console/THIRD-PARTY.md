# Third-party software in the console

Rule: permissive licences, with one documented test-only exception: axe-core (MPL-2.0), never distributed. Nothing here is shipped to the browser except the two fonts and the compiled output of Svelte and
SvelteKit (MIT); every other package is a build-time or test-time tool, and the Go binary embeds only the built files.

## Fonts (shipped, in `static/fonts/`)

Taken from the `@fontsource-variable` npm packages (version 5.3.0, latin subset), which repackage the upstream fonts.

| Font                      | Licence                   | Licence text                         |
| ------------------------- | ------------------------- | ------------------------------------ |
| Public Sans (variable)    | SIL Open Font License 1.1 | `static/fonts/OFL-PublicSans.txt`    |
| JetBrains Mono (variable) | SIL Open Font License 1.1 | `static/fonts/OFL-JetBrainsMono.txt` |

The console makes no request to a font host at run time (its Content-Security-Policy forbids it).

## npm dependencies (all `devDependencies`, exact versions in `package.json`, lockfile `pnpm-lock.yaml`)

| Package                      | Version | Licence     | Used for                           |
| ---------------------------- | ------- | ----------- | ---------------------------------- |
| svelte                       | 5.59.1  | MIT         | UI framework (runes)               |
| @sveltejs/kit                | 3.4.0   | MIT         | app framework, routing             |
| @sveltejs/adapter-static     | 4.0.1   | MIT         | static single-page build           |
| @sveltejs/vite-plugin-svelte | 7.3.2   | MIT         | Svelte in Vite                     |
| vite                         | 8.3.8   | MIT         | build tool                         |
| tailwindcss                  | 4.3.5   | MIT         | styles                             |
| @tailwindcss/vite            | 4.3.5   | MIT         | Tailwind in Vite                   |
| typescript                   | 6.0.3   | Apache-2.0  | types                              |
| svelte-check                 | 4.7.8   | MIT         | type check of Svelte files         |
| vitest                       | 5.1.1   | MIT         | unit and component tests           |
| jsdom                        | 26.1.0  | MIT         | DOM for the tests                  |
| @testing-library/svelte      | 5.4.2   | MIT         | component tests                    |
| @testing-library/jest-dom    | 6.9.1   | MIT         | test matchers                      |
| @testing-library/user-event  | 14.6.13 | MIT         | test input                         |
| axe-core                     | 4.14.1  | **MPL-2.0** | accessibility checks in tests      |
| @playwright/test             | 1.65.1  | Apache-2.0  | end-to-end tests                   |
| eslint                       | 9.39.5  | MIT         | lint                               |
| @eslint/js                   | 9.39.5  | MIT         | lint rules                         |
| eslint-plugin-svelte         | 3.24.1  | MIT         | lint rules                         |
| typescript-eslint            | 8.74.0  | MIT         | lint rules                         |
| globals                      | 17.14.0 | MIT         | lint globals                       |
| yaml                         | 2.9.1   | ISC         | parses the YAML view back in tests |
| prettier                     | 3.10.1  | MIT         | formatting                         |
| prettier-plugin-svelte       | 3.5.1   | MIT         | formatting                         |
| @types/node                  | 22.19.1 | MIT         | types for config files             |

### The one exception

`axe-core` is MPL-2.0, a file-level (weak) copyleft licence, not a permissive one. It is a test-only dev dependency: it is not
modified, never bundled into the app, and never distributed (it is not in the Go binary or the image), so MPL's obligations
(publish changes to MPL files you distribute) are not triggered. A maintainer must still confirm this reading, or replace it, before
a release. The transitive tree also holds a few MPL-2.0 build tools (for example `lightningcss` inside Tailwind), build-time only,
and packages under BlueOak-1.0.0, ISC, BSD and Python-2.0 (`argparse`), all permissive. `pnpm licenses list` prints the full tree.

## Go (services/console)

The service uses only the Go standard library. `gopkg.in/yaml.v3` (MIT and Apache-2.0) is a test-only dependency: the tests parse the
YAML the service writes and compare it with the data it was written from.

## Vulnerability checks

CI runs `pnpm audit --prod` (blocking) and `pnpm audit` over the whole tree (a report). Both were clean at the versions above.
