import { chromium } from "playwright";
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { createHash, generateKeyPairSync } from "node:crypto";

await mkdir("browser-results", {recursive: true});
const browser = await chromium.launch();
const errors = [];
const client = await browser.newPage({viewport: {width: 1440, height: 960}});
const admin = await browser.newPage({viewport: {width: 1440, height: 960}});
for (const page of [client, admin]) {
  page.on("pageerror", error => errors.push(error.message));
  page.setDefaultTimeout(15000);
}
async function fits(page) {
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "Page overflows horizontally");
}
try {
  await client.goto("http://127.0.0.1:8090");
  await client.getByLabel("Admin password").fill("browser-test-password");
  await client.getByRole("button", {name: "Log in", exact: true}).click();
  await client.getByRole("heading", {name: "Test library", exact: true}).waitFor();
  await client.screenshot({path: "browser-results/client-dashboard.png", fullPage: true});
  await client.getByRole("navigation", {name: "Main navigation"}).getByRole("link", {name: "My library", exact: true}).click();
  await client.waitForFunction(() => document.querySelectorAll("#library-rows tr").length === 100);
  await client.getByRole("button", {name: "Load more", exact: true}).click();
  await client.waitForFunction(() => document.querySelectorAll("#library-rows tr").length === 200);
  await client.getByLabel("Search title or filename").fill("smoke");
  await client.getByRole("button", {name: "Search", exact: true}).click();
  await client.waitForFunction(() => document.querySelectorAll("#library-rows tr").length === 1);
  assert.match(await client.locator("#library-rows").innerText(), /Smoke Quest/);
  await client.screenshot({path: "browser-results/client-library.png", fullPage: true});

  // A failed request must surface the server's error and offer a retry.
  let failedOnce = false;
  await client.route("**/api/library/items?**", async route => {
    if (!failedOnce) {
      failedOnce = true;
      await route.fulfill({status: 502, contentType: "application/json", body: JSON.stringify({error: "Temporary RomM outage"})});
    } else await route.continue();
  });
  await client.getByRole("button", {name: "Refresh", exact: true}).click();
  await client.getByText("Temporary RomM outage", {exact: true}).waitFor();
  await client.getByRole("button", {name: "Retry", exact: true}).click();
  await client.waitForFunction(() => document.querySelector("#library-status").textContent.includes("matching games shown"));
  await client.unroute("**/api/library/items?**");
  await client.setViewportSize({width: 390, height: 844});
  await fits(client);
  await client.screenshot({path: "browser-results/client-mobile.png", fullPage: true});
  await client.setViewportSize({width: 1440, height: 960});

  await client.goto("http://127.0.0.1:8090/inbox");
  await client.getByLabel("Game file", {exact: true}).setInputFiles({name: "bad.txt", mimeType: "text/plain", buffer: Buffer.from("invalid")});
  await client.locator("#upload_platform").selectOption("gb");
  await client.getByRole("button", {name: "Upload and import", exact: true}).click();
  await client.getByText("Fixture verifier rejected this file; choose a .gb cartridge", {exact: true}).waitFor();
  assert.match(client.url(), /\/inbox$/);
  await client.getByLabel("Game file", {exact: true}).setInputFiles({name: "Smoke Quest.gb", mimeType: "application/octet-stream", buffer: Buffer.alloc(65536)});
  await client.getByRole("button", {name: "Upload and import", exact: true}).click();
  await client.getByRole("link", {name: "View progress", exact: true}).click();
  await client.waitForFunction(() => document.querySelector("#activity-history").textContent.includes("Complete"));
  await client.screenshot({path: "browser-results/client-activity.png", fullPage: true});
  await client.goto("http://127.0.0.1:8090/swarm");
  await client.getByRole("button", {name: "Publish Inventory", exact: true}).click();
  await client.waitForFunction(() => document.querySelector("#publish-status-text").textContent.includes("Published revision 1"));
  console.log("Client: login, pagination, title search, retry, mobile layout, import errors, upload handoff, activity, publish feedback passed.");

  await admin.goto("http://127.0.0.1:8082");
  await admin.getByLabel("Username", {exact: true}).fill("browser-owner");
  await admin.getByLabel("Display name", {exact: true}).fill("Browser Owner");
  await admin.getByLabel("Password", {exact: true}).fill("browser-admin-password");
  await admin.getByLabel("Confirm password", {exact: true}).fill("browser-admin-password");
  await admin.getByRole("button", {name: "Create owner account", exact: true}).click();
  await admin.getByLabel("Swarm name", {exact: true}).fill("Test Swarm");
  await admin.getByRole("button", {name: "Create Swarm", exact: true}).click();
  await admin.getByRole("heading", {name: "Test Swarm", exact: true}).waitFor();
  const swarmURL = admin.url();
  await admin.getByLabel("Maximum uses", {exact: true}).fill("1");
  await admin.getByLabel("Expires after (days)", {exact: true}).fill("2");
  await admin.getByRole("button", {name: "Issue invitation", exact: true}).click();
  const code = await admin.locator(".code-display").textContent();
  const {publicKey} = generateKeyPairSync("ed25519");
  const public_key = publicKey.export({type: "spki", format: "der"}).subarray(-32).toString("base64");
  const enrollment = await fetch("http://127.0.0.1:8081/api/bridges/enroll", {
    method: "POST", headers: {"Content-Type": "application/json"},
    body: JSON.stringify({code, public_key})
  });
  assert.equal(enrollment.status, 201, "Bridge enrollment failed");
  await admin.goto(swarmURL);
  await admin.getByText("exhausted", {exact: true}).waitFor();
  await admin.getByRole("button", {name: "Edit", exact: true}).click();
  await admin.locator(".bridge-name-form input").fill("Living room");
  await admin.getByRole("button", {name: "Save", exact: true}).click();
  await admin.locator(".bridge-name-display").filter({hasText: "Living room"}).waitFor();
  await admin.getByLabel("Find a Bridge", {exact: true}).fill("missing");
  await admin.getByText("0 of 1 Bridges shown.", {exact: true}).waitFor();
  await admin.getByLabel("Find a Bridge", {exact: true}).fill("");

  const payload = Buffer.from("synthetic catalogue payload");
  const sha1 = createHash("sha1").update(payload).digest("hex");
  const md5 = createHash("md5").update(payload).digest("hex");
  const dat = '<datafile><header><name>Game Boy</name><version>1</version></header><game name="Smoke Quest (USA)"><rom name="Smoke Quest.gb" size="' + payload.length + '" crc="00000000" md5="' + md5 + '" sha1="' + sha1 + '"/></game></datafile>';
  await admin.locator("#catalogue-platform").selectOption("gb");
  await admin.locator("#catalogue-file").setInputFiles({name: "gb.dat", mimeType: "text/xml", buffer: Buffer.from(dat)});
  await admin.getByRole("button", {name: "Upload", exact: true}).click();
  await admin.getByText("Reference catalogue uploaded.", {exact: true}).waitFor();
  await admin.screenshot({path: "browser-results/admin-swarm.png", fullPage: true});
  await admin.setViewportSize({width: 390, height: 844});
  await fits(admin);
  await admin.screenshot({path: "browser-results/admin-mobile.png", fullPage: true});
  await admin.setViewportSize({width: 1440, height: 960});
  admin.on("dialog", dialog => dialog.accept());
  await admin.getByRole("button", {name: "Revoke", exact: true}).click();
  await admin.getByRole("button", {name: "Re-enroll", exact: true}).waitFor();
  await admin.getByRole("button", {name: "Remove", exact: true}).click();
  await admin.getByText("No Bridges enrolled in this Swarm yet.", {exact: true}).waitFor();
  await admin.locator("#confirm_name").fill("Test Swarm");
  await admin.getByRole("button", {name: "Delete Swarm", exact: true}).click();
  await admin.getByRole("heading", {name: "No Swarms yet.", exact: true}).waitFor();
  await admin.getByRole("button", {name: "Log out", exact: true}).click();
  await admin.getByRole("heading", {name: "Log in", exact: true}).waitFor();
  console.log("Admin: setup, Swarm creation, invitation limits, enrollment, exhausted state, rename, search, catalogue upload, mobile layout, revoke/remove, deletion with catalogue, logout passed.");
  assert.deepEqual(errors, [], "Browser JavaScript errors");
} finally {
  // Screenshots above contain fixture data only and never the one-time code.
  await browser.close();
}
