// Browser check of the password confirmation dialog (spec 5.7), run by
// TestE2EBrowserConfirm (internal/panel/e2e_browser_test.go, build tag
// e2e_browser), which starts a panel with a master and a replica and passes:
//   BASE          the panel's https URL
//   PASSWORD      admin's password
//   REPLICA_ID    the replica server's id
//   REPLICA_NAME  its name
// In Chromium it logs in, clicks "Resync ulang" on the replica page, then
// "Ya, resync ulang", and in the confirmation dialog first gives a wrong
// password (an error shows, nothing is submitted), then the right one with
// the typed name: the resync form must be posted with a confirm_token and
// the typed confirm_name. It prints the job page path the panel answered
// with. Needs Node with the playwright package and its Chromium.
"use strict";
const { chromium } = require("playwright");

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
  const id = need("REPLICA_ID");
  const name = need("REPLICA_NAME");
  const resyncURL = `${base}/replicas/${id}/resync`;

  const browser = await chromium.launch();
  try {
    // The panel's certificate comes from its own CA, which this browser
    // does not have installed.
    const context = await browser.newContext({ ignoreHTTPSErrors: true });
    const page = await context.newPage();
    const pageErrors = [];
    page.on("pageerror", (e) => pageErrors.push(e.message));
    const resyncPosts = [];
    page.on("request", (r) => {
      if (r.method() === "POST" && r.url() === resyncURL) {
        resyncPosts.push(r);
      }
    });

    await page.goto(`${base}/login`);
    await page.fill("#username", "admin");
    await page.fill('input[name="password"]', password);
    await Promise.all([page.waitForURL(`${base}/`), page.click('button:text-is("Masuk")')]);

    await page.goto(`${base}/replicas/${id}`);
    // "Resync ulang" reads the replica's databases, then the page opens
    // the dialog listing them.
    await Promise.all([
      page.waitForURL(`${base}/replicas/${id}?buka=resync`),
      page.getByRole("button", { name: "Resync ulang", exact: true }).click(),
    ]);
    const resync = page.locator("#modal-resync");
    await resync.waitFor({ state: "visible" });
    const trigger = resync.getByRole("button", { name: "Ya, resync ulang" });
    const danger = (await trigger.getAttribute("data-confirm-danger")) === "1";
    await trigger.click();

    const confirm = page.locator("#modal-konfirmasi");
    await confirm.waitFor({ state: "visible" });
    const ok = confirm.getByRole("button", { name: "Konfirmasi" });
    // The confirm button is pink (btn-error) only for a trigger marked
    // data-confirm-danger="1", else primary (design spec 5.5).
    const okClass = (await ok.getAttribute("class")) || "";
    if (okClass.includes("btn-error") !== danger || okClass.includes("btn-primary") === danger) {
      throw new Error(`confirm button class "${okClass}" for data-confirm-danger ${danger}`);
    }
    // The dialog has a second name field (promote's replica name), hidden here.
    await confirm.locator('input[x-ref="typed"]').fill(name);
    await confirm.locator('input[type="password"]').fill(password + "-salah");
    await ok.click();
    await confirm.locator("p[data-confirm-error]").filter({ hasText: /\S/ }).waitFor();
    if (resyncPosts.length !== 0) {
      throw new Error("the resync form was posted after a wrong password");
    }

    await confirm.locator('input[type="password"]').fill(password);
    const posted = page.waitForRequest((r) => r.method() === "POST" && r.url() === resyncURL);
    await ok.click();
    const req = await posted;
    const form = new URLSearchParams(req.postData() || "");
    if (!/^\S{16,}$/.test(form.get("confirm_token") || "")) {
      throw new Error("resync form posted without a confirm_token: " + req.postData());
    }
    if (form.get("confirm_name") !== name) {
      throw new Error("resync form posted confirm_name " + form.get("confirm_name"));
    }
    if (form.has("password")) {
      throw new Error("the password went into the resync form");
    }
    await page.waitForURL(/\/jobs\/[^/]+$/);
    if (pageErrors.length !== 0) {
      throw new Error("script errors on the page: " + pageErrors.join("; "));
    }
    console.log(new URL(page.url()).pathname);
  } finally {
    await browser.close();
  }
}

main().catch((e) => {
  console.error(e.stack || String(e));
  process.exit(1);
});
