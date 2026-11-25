# Third-party software in the console

Rule: only permissive licences. Nothing here is shipped to the browser except the two fonts and the compiled output of Svelte and
SvelteKit (MIT); every other package is a build-time or test-time tool, and the Go binary embeds only the built files.

## Fonts (shipped, in `static/fonts/`)

Taken from the `@fontsource-variable` npm packages (version 5.3.0, latin subset), which repackage the upstream fonts.

| Font                      | Licence                   | Licence text                         |
| ------------------------- | ------------------------- | ------------------------------------ |
| Public Sans (variable)    | SIL Open Font License 1.1 | `static/fonts/OFL-PublicSans.txt`    |
| JetBrains Mono (variable) | SIL Open Font License 1.1 | `static/fonts/OFL-JetBrainsMono.txt` |

The console makes no request to a font host at run time (its Content-Security-Policy forbids it).

## npm dependencies (all `devDependencies`, exact versions in `package.json`, lockfile `pnpm-lock.yaml`)

| Package                      | Version | Licence     | Used for                      |
| ---------------------------- | ------- | ----------- | ----------------------------- |
| svelte                       | 5.59.1  | MIT         | UI framework (runes)          |
| @sveltejs/kit                | 2.70.4  | MIT         | app framework, routing        |
| @sveltejs/adapter-static     | 3.0.11  | MIT         | static single-page build      |
| @sveltejs/vite-plugin-svelte | 6.2.4   | MIT         | Svelte in Vite                |
| vite                         | 7.3.8   | MIT         | build tool                    |
| tailwindcss                  | 4.3.5   | MIT         | styles                        |
| @tailwindcss/vite            | 4.3.5   | MIT         | Tailwind in Vite              |
| typescript                   | 5.9.3   | Apache-2.0  | types                         |
| svelte-check                 | 4.7.8   | MIT         | type check of Svelte files    |
| vitest                       | 3.2.7   | MIT         | unit and component tests      |
| jsdom                        | 26.1.0  | MIT         | DOM for the tests             |
| @testing-library/svelte      | 5.4.2   | MIT         | component tests               |
| @testing-library/jest-dom    | 6.9.1   | MIT         | test matchers                 |
| @testing-library/user-event  | 14.6.13 | MIT         | test input                    |
| axe-core                     | 4.14.1  | **MPL-2.0** | accessibility checks in tests |
| @playwright/test             | 1.65.1  | Apache-2.0  | end-to-end tests              |
| eslint                       | 9.39.5  | MIT         | lint                          |
| @eslint/js                   | 9.39.5  | MIT         | lint rules                    |
| eslint-plugin-svelte         | 3.24.1  | MIT         | lint rules                    |
| typescript-eslint            | 8.74.0  | MIT         | lint rules                    |
| globals                      | 17.14.0 | MIT         | lint globals                  |
| prettier                     | 3.10.1  | MIT         | formatting                    |
| prettier-plugin-svelte       | 3.5.1   | MIT         | formatting                    |
| @types/node                  | 22.19.1 | MIT         | types for config files        |

### Exception to review

`axe-core` is MPL-2.0, a file-level (weak) copyleft licence, not a permissive one. It was named in the task for the accessibility
tests. It is a test-only dev dependency, is not modified, is not bundled into the app and is not in the Go binary or the image, so
MPL's obligations (publish changes to MPL files) are not triggered. A maintainer must confirm this reading, or replace it, before a
release. The transitive tree also holds a few MPL-2.0 build tools (for example `lightningcss` inside Tailwind), build-time only, and
packages under BlueOak-1.0.0, ISC, BSD and Python-2.0 (`argparse`), all permissive. `pnpm licenses list` prints the full tree.
