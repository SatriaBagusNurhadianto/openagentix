import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { defineConfig } from 'vitest/config';
import type { Plugin } from 'vite';

/**
 * SvelteKit's generated root component renders its route announcer with an inline `style="..."` attribute, which the
 * console's Content-Security-Policy (`style-src 'self'`, no 'unsafe-inline') blocks. This swaps the attribute for a class
 * (see `.oa-announcer` in app.css). It fails loudly when SvelteKit changes that markup, so the CSP never has to be loosened.
 */
function announcerWithoutInlineStyle(): Plugin {
    const attr = / style="position: absolute; left: 0;[^"]*"/;
    return {
        name: 'announcer-without-inline-style',
        enforce: 'pre',
        transform(code, id) {
            if (!id.endsWith('.svelte-kit/generated/root.svelte')) return null;
            if (!attr.test(code))
                throw new Error(
                    'SvelteKit changed its route announcer markup: update announcerWithoutInlineStyle in vite.config.ts',
                );
            return code.replace(attr, ' class="oa-announcer"');
        },
    };
}

export default defineConfig({
    plugins: [announcerWithoutInlineStyle(), tailwindcss(), sveltekit(), svelteTesting()],
    server: {
        port: 5173,
        // dev loop: run the Go service in demo mode (CONSOLE_DEV_FAKE=1) and let Vite proxy the API to it
        proxy: { '/api': 'http://127.0.0.1:8080' },
    },
    test: {
        environment: 'jsdom',
        include: ['src/**/*.test.ts'],
        setupFiles: ['./src/test-setup.ts'],
        globals: false,
        css: false,
    },
});
