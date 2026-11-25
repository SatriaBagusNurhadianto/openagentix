import { defineConfig, devices } from '@playwright/test';

// The end-to-end tests run against the Go binary in demo mode (CONSOLE_DEV_FAKE=1) serving the built app, so they need
// `services/console/build-web.sh` and a Go toolchain, and no cluster.
const PORT = 18080;

export default defineConfig({
    testDir: './e2e',
    workers: 1,
    fullyParallel: false,
    retries: 0,
    timeout: 30_000,
    // in CI the HTML report (with traces and screenshots of failures) is uploaded as an artifact
    reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : [['list']],
    use: { baseURL: `http://127.0.0.1:${PORT}`, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
    projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
    webServer: {
        // `go run` builds the binary with the web app that build-web.sh copied into services/console/web/dist
        command: 'go run ./cmd/console',
        cwd: '../services/console',
        env: {
            CONSOLE_DEV_FAKE: '1',
            CONSOLE_ADDR: `127.0.0.1:${PORT}`,
            CONSOLE_DEV_FAKE_SETTLE: '0',
            CONSOLE_LOG_LEVEL: 'warn',
        },
        url: `http://127.0.0.1:${PORT}/healthz`,
        reuseExistingServer: false,
        timeout: 120_000,
    },
});
