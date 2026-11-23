// Headless smoke test of hack/dashboard/index.html with jsdom (from website/node_modules).
//   node smoke.js <base url of the dashboard server> <mode: data|empty|error>
// It loads the page, lets it query the (fake) API through the server and asserts what rendered. Every request the page makes is
// recorded and must stay on the page's own origin on loopback.
const path = require("path");
const { createRequire } = require("module");
const root = path.resolve(__dirname, "../../..");
const { JSDOM } = createRequire(path.join(root, "website", "package.json"))("jsdom");

const base = process.argv[2];
const mode = process.argv[3] || "data";
const origin = new URL(base).origin;
let failures = 0;
const check = (name, ok, extra) => {
  console.log((ok ? "  ok   " : "  FAIL ") + name + (ok || !extra ? "" : "\n       " + extra));
  if (!ok) failures++;
};

(async () => {
  const requested = [];
  const errors = [];
  const dom = await JSDOM.fromURL(base + "/", {
    runScripts: "dangerously",
    pretendToBeVisual: true,
    beforeParse(window) {
      window.fetch = (u, opts) => {
        const url = new URL(u, window.location.href);
        requested.push(url.href);
        return fetch(url, opts);
      };
      window.addEventListener("error", (e) => errors.push(String(e.message)));
    },
  });
  const { window } = dom;
  const doc = window.document;
  const status = () => doc.getElementById("status").textContent;
  for (let i = 0; i < 100 && !/^(updated|\d+ of \d+ queries failed)/.test(status()); i++) await new Promise((r) => setTimeout(r, 50));

  check("the page settled (" + status() + ")", /^(updated|\d+ of \d+ queries failed)/.test(status()));
  check("no script error", errors.length === 0, errors.join("; "));
  check("every request stays on the page's origin on loopback", requested.length > 0 && requested.every((u) => u.startsWith(origin + "/")), requested.filter((u) => !u.startsWith(origin + "/")).join(", "));
  check("the page's own origin is loopback", /^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin), origin);

  const sections = [...doc.querySelectorAll("main > section")];
  const titles = sections.map((s) => s.querySelector("h2").textContent);
  const want = ["Cost per agent (USD)", "Tokens per agent", "Model requests and errors", "Tool calls by tool and outcome", "Knowledge base searches", "Graph extraction", "Notifications", "Scrape target health"];
  check("all eight panels render", want.every((t) => titles.includes(t)), titles.join(" | "));
  check("every panel shows its PromQL", sections.every((s) => /PromQL:/.test(s.textContent) && s.querySelector(".cap code").textContent.length >= 2));
  check("the cost caption names the VictoriaMetrics first-sample quirk", /first sample as an increase/.test(sections[0].textContent));
  check("no panel is a blank box", sections.every((s) => s.querySelector(".bars, svg.chart, table, .empty, .err, .tiles")));

  const text = (sel) => [...doc.querySelectorAll(sel)].map((e) => e.textContent).join("\n");
  if (mode === "data") {
    check("no 'no data yet' placeholder when data exists", doc.querySelectorAll("section .empty").length === 0, text("section .empty"));
    check("cost per agent: assistant and its USD figure", /assistant/.test(sections[0].textContent) && /\$0\.0123/.test(sections[0].textContent));
    check("tokens per agent: input and output rows", /assistant \/ input/.test(sections[1].textContent) && /assistant \/ output/.test(sections[1].textContent));
    check("model panel: request and error tiles plus a line chart", /120/.test(sections[2].textContent) && /2\.0 %/.test(sections[2].textContent) && sections[2].querySelectorAll("svg.chart polyline").length === 1);
    check("tools panel: tool and status", /osv_query_vulnerability \/ success/.test(sections[3].textContent));
    check("kb panel: modes, and p50 and p95 lines", /hybrid \/ ok/.test(sections[4].textContent) && sections[4].querySelectorAll("svg.chart polyline").length === 2);
    check("graph panel: tokens, pending, job states", /18\.4 k|18400/.test(sections[5].textContent) && /done/.test(sections[5].textContent) && /retry/.test(sections[5].textContent));
    check("notify panel: kind and outcome", /telegram \/ delivered/.test(sections[6].textContent));
    const rows = [...doc.querySelectorAll("table tbody tr")].map((r) => r.textContent);
    check("target table: three targets, the down one first and marked", rows.length === 3 && /DOWN/.test(rows[0]) && /notify/.test(rows[0]), rows.join(" | "));
    // window selector
    requested.length = 0;
    doc.querySelector('button[data-w="24h"]').click();
    for (let i = 0; i < 100 && !/^updated/.test(status()); i++) await new Promise((r) => setTimeout(r, 50));
    check("24 h selector re-queries with a 24h window", requested.some((u) => /increase\(agentgateway_gen_ai_client_cost_usd_total%5B24h%5D\)/.test(u) || /cost_usd_total%5B24h%5D/.test(u)), requested.slice(0, 2).join(", "));
    check("24 h selector marks the button pressed", doc.querySelector('button[data-w="24h"]').getAttribute("aria-pressed") === "true");
  } else if (mode === "empty") {
    const emptyPanels = sections.filter((s) => s.querySelector(".empty"));
    check("every panel says 'no data yet' (" + emptyPanels.length + " of " + sections.length + ")", emptyPanels.length === sections.length && /no data yet/.test(text("section .empty")));
    check("no broken chart or table with no data", doc.querySelectorAll("svg.chart, table").length === 0);
    check("no NaN or undefined text anywhere", !/NaN|undefined|null/.test(doc.querySelector("main").textContent));
  } else if (mode === "error") {
    const failed = sections.filter((s) => s.querySelector(".err"));
    check("the failing query is shown as an error, not as no data", failed.length >= 1 && /query failed/.test(failed[0].textContent), text("section .err"));
    check("the other panels still render", sections.filter((s) => s.querySelector(".bars, svg.chart, table")).length >= 5);
    check("the status line counts the failures", /queries failed/.test(status()), status());
  }
  window.close();
  console.log(failures === 0 ? "smoke: ok" : "smoke: " + failures + " failure(s)");
  process.exit(failures === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(2); });
