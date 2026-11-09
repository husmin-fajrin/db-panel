// Screenshot tour of the panel UI, run by TestE2EScreenshotTour
// (internal/panel/e2e_screens_test.go, build tag e2e_browser), which starts
// a panel with a master and two replicas and calls this script once per
// PHASE, with:
//   BASE          the panel's https URL
//   PASSWORD      admin's password
//   OUT           the directory the PNG files go to
//   MASTER_ID     the (first) master's server id
//   REPLICA_A_ID  replika-a, the promote's candidate
//   REPLICA_B_ID  replika-b
//   JOB_ID        the promote job (phases promote and akhir)
//   PHASE         sebelum: before the promote (01-07); promote: right after
//                 it (08-09); akhir: after the leftover check (10-15);
//                 replikasi: after the test added a second topology (16)
// Each phase takes every one of its screenshots four times: in both themes
// (gelap first, the panel's main theme, then terang) and at two widths
// (1440x900 and a 390x844 phone), device scale factor 2, as
// <nn>-<halaman>-<tema>-<lebar>.png. The theme is fixed per pass through
// both prefers-color-scheme and localStorage.tema, so the panel's default
// theme setting does not matter. Nothing is submitted besides the login.
// At 390 px a page wider than the viewport (horizontal page scroll) is
// reported on stdout as "OVERFLOW <file>: <n> px" (the run goes on, so the
// remaining screenshots are still taken).
// Needs Node with the playwright package and its Chromium.
"use strict";
const path = require("path");
const { chromium } = require("playwright");

const TEMA = [
  { name: "gelap", scheme: "dark" },
  { name: "terang", scheme: "light" },
];
const LEBAR = [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
];

function need(name) {
  const v = process.env[name];
  if (!v) {
    throw new Error(name + " is not set");
  }
  return v;
}

async function main() {
  const base = need("BASE");
  const password = need("PASSWORD");
  const out = need("OUT");
  const phase = need("PHASE");
  const master = need("MASTER_ID");
  const ra = need("REPLICA_A_ID");
  const rb = need("REPLICA_B_ID");
  if (!["sebelum", "promote", "akhir", "replikasi"].includes(phase)) {
    throw new Error("unknown PHASE " + phase);
  }

  const browser = await chromium.launch();
  try {
    for (const tema of TEMA) {
      for (const lebar of LEBAR) {
        await pass(browser, tema, lebar);
      }
    }
  } finally {
    await browser.close();
  }

  async function pass(browser, tema, lebar) {
    // The panel's certificate comes from its own CA, which this browser
    // does not have installed.
    const context = await browser.newContext({
      ignoreHTTPSErrors: true,
      viewport: lebar,
      deviceScaleFactor: 2,
      locale: "id-ID",
      colorScheme: tema.scheme,
    });
    await context.addInitScript((t) => {
      try {
        window.localStorage.setItem("tema", t);
      } catch (e) {
        // Storage blocked: prefers-color-scheme still picks the theme.
      }
    }, tema.name);
    try {
      const page = await context.newPage();
      const pageErrors = [];
      page.on("pageerror", (e) => pageErrors.push(e.message));

      async function shot(name, opts = {}) {
        const file = path.join(out, `${name}-${tema.name}-${lebar.width}.png`);
        const overflow = await page.evaluate(
          () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
        );
        if (overflow > 0) {
          console.log(`OVERFLOW ${file}: ${overflow} px`);
        }
        await page.screenshot({ path: file, fullPage: opts.fullPage !== false });
        console.log("screenshot " + file);
      }

      // open loads a page, fails on an error status, and waits for the
      // fragments the page loads after it (htmx) to settle.
      async function open(p, waitText) {
        const resp = await page.goto(base + p, { waitUntil: "load" });
        if (!resp || resp.status() >= 400) {
          throw new Error(`GET ${p} = ${resp ? resp.status() : "no response"}`);
        }
        if (waitText) {
          await page.getByText(waitText).first().waitFor({ timeout: 30000 });
        }
        await page.waitForTimeout(1200);
      }

      await page.goto(`${base}/login`);
      await page.locator("#username").waitFor();
      if (phase === "sebelum") {
        await page.waitForTimeout(500);
        await shot("01-login");
      }
      await page.fill("#username", "admin");
      await page.fill('input[name="password"]', password);
      await Promise.all([page.waitForURL(`${base}/`), page.click('button:text-is("Masuk")')]);

      if (phase === "sebelum") {
        await page.waitForTimeout(2000);
        await shot("02-dashboard");
        await open("/servers");
        await shot("03-servers");
        await open(`/servers/${master}`);
        await shot("04-server-detail");
        await open(`/masters/${master}`);
        await shot("05-master-detail");
        await open(`/replicas/${ra}`);
        await shot("06-replica-detail");
        await open(`/masters/${master}/promote`, "Terencana (master hidup)");
        // The mode is planned (the master is up); pick replika-a, as the
        // promote that follows.
        const cand = page.locator(`input[name="candidate"][value="${ra}"]`);
        if ((await cand.count()) > 0 && (await cand.first().isEnabled())) {
          await cand.first().check();
        }
        await page.waitForTimeout(500);
        await shot("07-promote");
      } else if (phase === "promote") {
        const job = need("JOB_ID");
        await open(`/jobs/${job}`);
        await shot("08-job-promote");
        await open(`/masters/${ra}/cutover`, "app_e2e");
        await shot("09-cutover");
      } else if (phase === "akhir") {
        await open(`/servers/${master}`, "Master lama");
        await shot("10-old-master");
        await open("/alerts");
        await shot("11-alerts");
        await open("/audit");
        await shot("12-audit");
        await open("/settings");
        await shot("13-settings");
        await open("/jobs");
        await shot("14-jobs");
        // The password confirmation dialog of "Lepas replika", not submitted.
        await open(`/replicas/${rb}`);
        await page.getByRole("button", { name: "Lepas replika", exact: true }).click();
        const detach = page.locator("#modal-detach");
        await detach.waitFor({ state: "visible" });
        await detach.locator('input[name="mode"][value="keep"]').check();
        await detach.getByRole("button", { name: "Ya, lepas" }).click();
        const confirm = page.locator("#modal-konfirmasi");
        await confirm.waitFor({ state: "visible" });
        await page.waitForTimeout(500);
        await shot("15-dialog-konfirmasi", { fullPage: false });
      } else if (phase === "replikasi") {
        // Two topologies, so /replikasi is the list, not a redirect.
        await open("/replikasi", "db-gudang");
        if (!page.url().endsWith("/replikasi")) {
          throw new Error("/replikasi redirected to " + page.url());
        }
        await shot("16-replikasi");
      }
      if (pageErrors.length !== 0) {
        throw new Error(`script errors on the page (${tema.name}, ${lebar.width}): ` + pageErrors.join("; "));
      }
    } finally {
      await context.close();
    }
  }
}

main().catch((e) => {
  console.error(e.stack || String(e));
  process.exit(1);
});
