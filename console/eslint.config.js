import js from '@eslint/js';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';
import ts from 'typescript-eslint';

export default ts.config(
    { ignores: ['build/', '.svelte-kit/', 'node_modules/', 'playwright-report/', 'test-results/'] },
    js.configs.recommended,
    ...ts.configs.recommended,
    ...svelte.configs['flat/recommended'],
    {
        languageOptions: { globals: { ...globals.browser, ...globals.node } },
        rules: {
            '@typescript-eslint/no-explicit-any': 'error',
            '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
            // plain links to internal routes are fine in a single-page app served from the root
            'svelte/no-navigation-without-resolve': 'off',
            'svelte/require-each-key': 'off',
        },
    },
    {
        files: ['**/*.svelte', '**/*.svelte.ts', '**/*.svelte.js'],
        languageOptions: { parserOptions: { parser: ts.parser } },
    },
);
