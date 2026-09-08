import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await page.goto("/dashboard/");
  if (await page.getByRole("heading", { name: "Sign in" }).isVisible()) {
    await page.getByLabel("Username").fill("admin");
    await page.getByLabel("Password").fill("admin123");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
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
