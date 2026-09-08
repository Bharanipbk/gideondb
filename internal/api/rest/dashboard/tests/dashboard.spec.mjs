import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await page.goto("/dashboard/");
  if (await page.getByRole("heading", { name: "Welcome back" }).isVisible()) {
    await page.getByLabel("Username").fill("admin");
    await page.getByLabel("Password", { exact: true }).fill("browser-test-password");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
});

test("theme switch persists across dashboard reloads", async ({ page }) => {
  const initial = await page.locator("html").getAttribute("data-theme");
  const expected = initial === "dark" ? "light" : "dark";
  const before = await page.evaluate(() => ({ body: getComputedStyle(document.body).backgroundColor, sidebar: getComputedStyle(document.querySelector(".app-sidebar")).backgroundColor, card: getComputedStyle(document.querySelector(".panel")).backgroundColor, text: getComputedStyle(document.body).color }));
  await page.locator("header").getByRole("button", { name: `Switch to ${expected} theme` }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", expected);
  const after = await page.evaluate(() => ({ body: getComputedStyle(document.body).backgroundColor, sidebar: getComputedStyle(document.querySelector(".app-sidebar")).backgroundColor, card: getComputedStyle(document.querySelector(".panel")).backgroundColor, text: getComputedStyle(document.body).color }));
  expect(after.body).not.toBe(before.body);
  expect(after.sidebar).not.toBe(before.sidebar);
  expect(after.card).not.toBe(before.card);
  expect(after.text).not.toBe(before.text);
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", expected);
});

test("dashboard uses curved cards without decorative header rules", async ({ page }) => {
  await expect(page.locator(".panel").first()).toHaveCSS("border-radius", "14px");
  await expect(page.locator(".stats article").first()).toHaveCSS("border-radius", "12px");
  await expect(page.locator(".panel-head").first()).toHaveCSS("border-bottom-width", "0px");
  await expect(page.locator(".brand-link")).toHaveCSS("border-bottom-width", "0px");
});

test("dashboard surfaces use a flat design without drop shadows", async ({ page }) => {
  for (const selector of [".app-header", ".app-sidebar", ".panel", ".stats article", "button"]) {
    await expect(page.locator(selector).first()).toHaveCSS("box-shadow", "none");
  }
});

test("logs paginate retained events", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name.startsWith("mobile"), "covered once on desktop");
  await page.evaluate(() => Promise.all(Array.from({ length: 16 }, () => fetch("/v1/health"))));
  await page.getByRole("button", { name: "Refresh" }).click();
  await page.getByRole("button", { name: /Logs/ }).click();
  await expect(page.locator("#log-events .log-row")).toHaveCount(10);
  await expect(page.locator("#logs-page-status")).toContainText("Page 1 of");
  await expect(page.locator("#logs-previous")).toBeDisabled();
  await expect(page.locator("#logs-next")).toBeEnabled();
  await page.locator("#logs-next").click();
  await expect(page.locator("#logs-page-status")).toContainText("Page 2 of");
  await expect(page.locator("#logs-previous")).toBeEnabled();
});

test("header aligns with the sidebar brand without a top gap", async ({ page }) => {
  const layout = await page.evaluate(() => {
    const sidebar = document.querySelector(".app-sidebar").getBoundingClientRect();
    const brand = document.querySelector(".brand-link").getBoundingClientRect();
    const header = document.querySelector(".app-header").getBoundingClientRect();
    return {
      sidebarTop: sidebar.top,
      brandTop: brand.top,
      headerTop: header.top,
      brandHeight: brand.height,
      headerHeight: header.height,
    };
  });

  expect(layout.sidebarTop).toBe(0);
  expect(layout.brandTop).toBe(0);
  expect(layout.headerTop).toBe(0);
  expect(Math.abs(layout.brandHeight - layout.headerHeight)).toBeLessThanOrEqual(1);
});

test("navigation exposes operational context and resilience signals", async ({ page }, testInfo) => {
  if (testInfo.project.name.startsWith("mobile")) {
    await page.getByRole("button", { name: "Open navigation" }).click();
  }
  await page.getByRole("button", { name: /Resilience/ }).click();
  await expect(page.locator("#page-title")).toHaveText("Resilience");
  await expect(page.locator("#page-context")).toHaveText("RECOVERY");
  await expect(page.locator("#alert-list .alert-item")).toHaveCount(1);
  await expect(page.getByRole("link", { name: "Open recovery runbook" })).toBeVisible();
});

test("mobile navigation opens and closes without obscuring the selected view", async ({ page }, testInfo) => {
  test.skip(!testInfo.project.name.startsWith("mobile"), "mobile-only interaction");
  const menu = page.getByRole("button", { name: "Open navigation" });
  await expect(menu).toHaveAttribute("aria-expanded", "false");
  await menu.click();
  await expect(menu).toHaveAttribute("aria-expanded", "true");
  await page.getByRole("button", { name: /Collections/ }).click();
  await expect(menu).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator("#page-title")).toHaveText("Collections");
});

test("API key stays tab-scoped and refresh remains operational", async ({ page }) => {
  await page.getByRole("button", { name: "API key", exact: true }).click();
  await page.getByLabel("Bearer token").fill("browser-test-key");
  await page.getByRole("button", { name: "Save for session" }).click();
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem("gideondb-api-key"))).toBe("browser-test-key");
  await expect(page.locator("#connection-label")).toHaveText("Connected");
});

test("collection deletion requires exact typed confirmation", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name.startsWith("mobile"), "covered once on desktop");
  const name = `browser-guard-${Date.now()}`;
  const created = await page.evaluate(async collection => {
    const response = await fetch("/v1/collections", { method: "POST", headers: { "Content-Type": "application/json", "X-GideonDB-CSRF": sessionStorage.getItem("gideondb-dashboard-csrf") }, body: JSON.stringify(collection) });
    return response.ok;
  }, { name, dimension: 2, metric: "cosine", shard_count: 1 });
  expect(created).toBeTruthy();
  await page.reload();
  await page.getByRole("button", { name: /Collections/ }).click();
  page.once("dialog", dialog => dialog.accept("not-the-name"));
  const catalogCard = page.locator("#collections-grid article.collection", { hasText: name });
  await catalogCard.getByRole("button", { name: "Delete" }).click();
  await expect(catalogCard).toBeVisible();
});
