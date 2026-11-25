import { expect, test, type Page } from '@playwright/test';

const TOKEN = 'demo-token';
const SENTENCES = [
    'the definition lists tool "knowledge_kb_get_document" which is not in profile "lookup" (the profile offers: knowledge_kb_search, osv_query_vulnerability, osv_get_vulnerability, execute_tool_script)',
    'egress.allow entry "https://services.nvd.nist.gov" is not matched by the platform policy',
];

/** Collects what the strict Content-Security-Policy would complain about, and browser errors. */
async function watch(page: Page, allow: RegExp[] = []) {
    const problems: string[] = [];
    await page.addInitScript(() => {
        (window as unknown as { __csp: string[] }).__csp = [];
        document.addEventListener('securitypolicyviolation', (e) => {
            (window as unknown as { __csp: string[] }).__csp.push(
                `${e.violatedDirective} ${e.blockedURI} ${e.sample ?? ''}`,
            );
        });
    });
    page.on('console', (m) => {
        if (m.type() === 'error' && !allow.some((r) => r.test(m.text()))) problems.push(`console: ${m.text()}`);
    });
    page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
    return async () => {
        const csp = await page.evaluate(() => (window as unknown as { __csp: string[] }).__csp);
        expect(csp, 'securitypolicyviolation events').toEqual([]);
        expect(problems, 'console errors').toEqual([]);
    };
}

async function signIn(page: Page, token = TOKEN) {
    await page.goto('/');
    await page.getByLabel('Console token').fill(token);
    await page.getByRole('button', { name: 'Sign in' }).click();
}

async function goTo(page: Page, href: string) {
    const menu = page.getByRole('button', { name: 'Open navigation' });
    if (await menu.isVisible()) await menu.click();
    await page.locator(`nav[aria-label="Main"] a[href="${href}"]`).first().click();
}

test('the app shell shows no data before sign-in, and a wrong token is refused', async ({ page }) => {
    const done = await watch(page, [/status of 401/]); // the refusal itself is logged by the browser
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    await expect(page.locator('body')).not.toContainText('researcher');
    await page.getByLabel('Console token').fill('wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('alert')).toContainText('The console token was not accepted');
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain('wrong');
    await done();
});

test('sign in, see the registry with the demo data, and the token is never stored', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await expect(page.getByText('Demo data')).toBeVisible();
    await goTo(page, '/agents');
    const rows = page.locator('#agentTable tbody tr');
    await expect(rows).toHaveCount(5);
    await expect(page.locator('tr[data-agent="researcher"]')).toContainText('Ready');
    await expect(page.locator('tr[data-agent="release-notes"]')).toContainText('Pending');
    await expect(page.locator('tr[data-agent="cve-triage"]')).toContainText('Refused');
    await expect(page.locator('tr[data-agent="assistant"]')).toContainText('Chart');
    await expect(page.getByTestId('used')).toContainText('3 of 5 definitions used');
    expect(
        await page.evaluate(() => JSON.stringify(localStorage) + JSON.stringify(sessionStorage) + document.cookie),
    ).not.toContain(TOKEN);
    await page.getByLabel('Filter agents by name').fill('cve');
    await expect(rows).toHaveCount(1);
    await page
        .getByRole('button', { name: 'Settings' })
        .or(page.getByRole('link', { name: 'Settings' }))
        .first()
        .click();
    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    await done();
});

test('every other navigation entry is an honest not-available page', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    for (const [href, title] of [
        ['/skills', 'Skills'],
        ['/models', 'Models'],
        ['/tools', 'Tools'],
        ['/knowledge', 'Knowledge'],
        ['/channels', 'Channels and triggers'],
        ['/schedules', 'Schedules'],
        ['/traces', 'Traces'],
        ['/costs', 'Costs'],
        ['/memory', 'Memory'],
        ['/governance', 'Governance'],
        ['/', 'Overview'],
    ]) {
        await goTo(page, href!);
        await expect(page.getByRole('heading', { level: 1, name: title })).toBeVisible();
        await expect(page.getByText('Why it is not available yet')).toBeVisible();
    }
    await done();
});

test('a refused agent shows both violation sentences word for word', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await goTo(page, '/agents');
    await page.locator('tr[data-agent="cve-triage"] a').click();
    const list = page.getByTestId('violations');
    await expect(list.getByRole('listitem')).toHaveText(SENTENCES);
    await expect(page.getByText('2 rules refused this definition')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Refused by the platform policy' })).toBeVisible();
    await done();
});

test('open researcher, edit the description, save', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await goTo(page, '/agents');
    await page.locator('tr[data-agent="researcher"] a').click();
    await expect(page.getByRole('heading', { level: 1, name: 'researcher' })).toBeVisible();
    await expect(page.getByText('Status is for generation 3, the current one.')).toBeVisible();
    const desc = page.getByLabel('Description');
    await desc.fill('Looks things up and cites what it found, briefly');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: 'Saved researcher' })).toBeVisible();
    await expect(page.getByText(/Status is for generation 3; you are looking at generation 4/)).toBeVisible();
    await page.getByRole('button', { name: 'View YAML' }).click();
    await expect(page.getByRole('dialog')).toContainText('Looks things up and cites what it found, briefly');
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await done();
});

test('create a new agent and see it Pending', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await goTo(page, '/agents');
    await page.getByRole('button', { name: /New agent/ }).click();
    await page.getByLabel('Name', { exact: true }).fill('e2e-agent');
    await page.getByLabel('Description').fill('Made by the end-to-end test');
    await page.getByLabel('Instructions').fill('Answer in one sentence.');
    await page.getByRole('button', { name: 'Create agent' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'e2e-agent' })).toBeVisible();
    await expect(page.locator('.ph .badge')).toHaveText('Pending');
    await goTo(page, '/agents');
    await expect(page.locator('tr[data-agent="e2e-agent"]')).toContainText('Pending');
    await expect(page.getByTestId('used')).toContainText('4 of 5');
    await done();
});

test('a draft that the policy would refuse is flagged before saving', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await goTo(page, '/agents');
    await page.locator('tr[data-agent="researcher"] a').click();
    await page.getByPlaceholder('https://host[:port]').fill('https://services.nvd.nist.gov');
    await page.getByRole('button', { name: 'Add' }).click();
    await expect(page.getByText('This draft would be refused')).toBeVisible();
    await expect(page.getByTestId('predicted')).toContainText('is not matched by the platform policy');
    await expect(page.getByRole('button', { name: 'Save anyway' })).toBeVisible();
    await done();
});

test('delete asks for the name, then removes the agent', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await goTo(page, '/agents');
    await page.locator('tr[data-agent="e2e-agent"] a').click();
    await page.getByRole('button', { name: 'Delete', exact: true }).click();
    const dlg = page.getByRole('alertdialog');
    await expect(dlg.getByRole('button', { name: 'Cancel' })).toBeFocused();
    const go = dlg.getByRole('button', { name: 'Delete agent' });
    await expect(go).toHaveAttribute('aria-disabled', 'true');
    await dlg.getByLabel('Type e2e-agent to confirm').fill('e2e-agent');
    await go.click();
    await expect(page.getByRole('heading', { level: 1, name: 'Agents' })).toBeVisible();
    await expect(page.locator('tr[data-agent="e2e-agent"]')).toHaveCount(0);
    await done();
});

test('pause and resume an agent', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await goTo(page, '/agents');
    await page.locator('tr[data-agent="researcher"] a').click();
    await page.getByRole('button', { name: 'Pause', exact: true }).click();
    await expect(page.locator('.ph .badge')).toHaveText('Paused');
    await goTo(page, '/agents');
    await expect(page.locator('tr[data-agent="researcher"]')).toContainText('Paused');
    await page.locator('tr[data-agent="researcher"] a').click();
    await page.getByRole('button', { name: 'Resume', exact: true }).click();
    await expect(page.locator('.ph .badge')).toHaveText('Ready');
    await done();
});

test('chart agents open read-only', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await goTo(page, '/agents');
    await page.locator('tr[data-agent="assistant"] a').click();
    await expect(page.getByText(/This agent comes from chart values/)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Save' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Delete', exact: true })).toHaveCount(0);
    await done();
});

test('the create flow works with the keyboard alone', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    await goTo(page, '/agents');
    // `n` is the shortcut for a new agent
    await page.locator('body').click({ position: { x: 5, y: 5 } });
    await page.keyboard.press('n');
    await expect(page.getByRole('heading', { level: 1, name: 'New agent' })).toBeVisible();
    await page.getByLabel('Name', { exact: true }).focus();
    await page.keyboard.type('kb-agent');
    await page.keyboard.press('Tab'); // namespace is read only, so the next stop is the description
    await page.keyboard.press('Tab');
    await page.keyboard.type('Keyboard only');
    await page.keyboard.press('Tab');
    await page.keyboard.type('Say hello.');
    // Ctrl+S saves
    await page.keyboard.press('Control+s');
    await expect(page.getByRole('heading', { level: 1, name: 'kb-agent' })).toBeVisible();
    // tabs move with the arrow keys
    await page.getByRole('tab', { name: 'Build' }).focus();
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await expect(page.getByRole('tab', { name: 'Deploy' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByText('Caller rule to hand to an operator')).toBeVisible();
    await done();
});

test('the theme toggle persists', async ({ page }) => {
    const done = await watch(page);
    await signIn(page);
    const html = page.locator('html');
    const before = await html.evaluate((e) => e.classList.contains('dark'));
    await page.locator('#theme-toggle').click();
    await expect(html).toHaveClass(before ? /^((?!dark).)*$/ : /dark/);
    const stored = await page.evaluate(() => localStorage.getItem('oa-theme'));
    expect(stored).toBe(before ? 'light' : 'dark');
    await page.reload();
    expect(await html.evaluate((e) => e.classList.contains('dark'))).toBe(!before);
    await done();
});

test.describe('390 px wide', () => {
    test.use({ viewport: { width: 390, height: 844 } });
    test('no page scrolls sideways, and the drawer works', async ({ page }) => {
        const done = await watch(page);
        await signIn(page);
        const noScroll = async (what: string) => {
            const w = await page.evaluate(() => ({
                sw: document.documentElement.scrollWidth,
                cw: document.documentElement.clientWidth,
            }));
            expect(w.sw, `${what} scrolls sideways`).toBeLessThanOrEqual(w.cw);
        };
        await noScroll('overview');
        await page.getByRole('button', { name: 'Open navigation' }).click();
        await expect(page.locator('nav[aria-label="Main"]')).toBeInViewport();
        await page.keyboard.press('Escape');
        await goTo(page, '/agents');
        await expect(page.locator('#agentTable')).toBeVisible();
        await noScroll('registry');
        await page.locator('tr[data-agent="researcher"] a').click();
        await expect(page.getByText('Status is for generation')).toBeVisible();
        await noScroll('builder');
        await page.getByRole('tab', { name: 'Deploy' }).click();
        await noScroll('deploy');
        await goTo(page, '/models');
        await noScroll('models');
        await done();
    });
});
