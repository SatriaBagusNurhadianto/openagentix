import adapter from '@sveltejs/adapter-static';

/** @type {import('@sveltejs/kit').Config} */
const config = {
    compilerOptions: { runes: true },
    kit: {
        // A single-page app: every route is rendered in the browser and index.html is the fallback the Go service serves.
        adapter: adapter({ pages: 'build', assets: 'build', fallback: 'index.html', precompress: false, strict: true }),
        // The console's CSP has no 'unsafe-inline' for styles, so no stylesheet may be inlined into the page.
        inlineStyleThreshold: 0,
        alias: { $lib: 'src/lib' },
    },
};

export default config;
