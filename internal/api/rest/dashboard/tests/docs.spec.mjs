import { expect, test } from "@playwright/test";

test("documentation theme switches and persists", async ({ page }) => {
  await page.goto("/docs/");
  await expect(page.getByRole("heading", { name: /GideonDB/ })).toBeVisible();
  const initial = await page.locator("html").getAttribute("data-theme");
  const next = initial === "dark" ? "light" : "dark";
  const before = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);

  await page.getByRole("button", { name: `Switch to ${next} theme` }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", next);
  await expect.poll(() => page.evaluate(() => localStorage.getItem("gideondb-theme"))).toBe(next);
  const after = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  expect(after).not.toBe(before);

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", next);
});

test("dashboard header is rectangular", async ({ page }) => {
  await page.goto("/dashboard/");
  if (await page.getByRole("heading", { name: "Welcome back" }).isVisible()) {
    await page.getByLabel("Username").fill("admin");
    await page.getByLabel("Password", { exact: true }).fill("browser-test-password");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.locator(".app-header")).toHaveCSS("border-radius", "0px");
});
