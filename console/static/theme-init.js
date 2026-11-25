// Runs before the first paint so that the page does not flash the wrong theme. It is a file, not an inline script,
// because the console's Content-Security-Policy allows scripts from its own origin only.
(function () {
    try {
        var t = localStorage.getItem('oa-theme');
        var dark = t === 'dark' || (t !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
        if (dark) document.documentElement.classList.add('dark');
    } catch {
        /* storage can be blocked; the system setting is used then */
    }
})();
