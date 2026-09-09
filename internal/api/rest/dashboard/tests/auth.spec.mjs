import { expect, test } from "@playwright/test";

test("bootstrap login requires password replacement", async ({ page }) => {
  await page.route("**/v1/dashboard/session", route => route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({ authenticated: true, csrf_token: "test-csrf", must_change_password: true }),
  }));
  await page.goto("/dashboard/");
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password", { exact: true }).fill("bootstrap-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Replace bootstrap password" })).toBeVisible();
  await expect(page.getByLabel("New password", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Confirm new password")).toBeVisible();
});

test("login theme switch is accessible and persistent", async ({ page }) => {
  await page.goto("/dashboard/");
  const initial = await page.locator("html").getAttribute("data-theme");
  const expected = initial === "dark" ? "light" : "dark";
  await page.getByRole("button", { name: `Switch to ${expected} theme` }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", expected);
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", expected);
});

test("login presents a flat responsive operator workspace", async ({ page }, testInfo) => {
  await page.goto("/dashboard/");
  await expect(page.locator(".login-shell")).toHaveCSS("box-shadow", "none");
  await expect(page.locator(".login-panel")).toHaveCSS("box-shadow", "none");
  await expect(page.locator(".login-panel svg.radix-icon")).toHaveCount(7);
  const formWidth = await page.locator("#login-form").evaluate(node => node.getBoundingClientRect().width);
  expect(formWidth).toBeLessThanOrEqual(410);
  if (!testInfo.project.name.startsWith("mobile")) {
    await expect(page.getByText("Operator workspace", { exact: true })).toBeVisible();
    await expect(page.locator(".login-capabilities li", { hasText: "Live cluster and shard health" })).toBeVisible();
  }
});

test("login shell fills the complete viewport", async ({ page }) => {
  await page.goto("/dashboard/");
  const dimensions = await page.locator(".login-shell").evaluate(node => {
    const box = node.getBoundingClientRect();
    return { left: box.left, width: box.width, height: box.height, viewportWidth: innerWidth, viewportHeight: innerHeight, pageWidth: document.documentElement.scrollWidth, pageHeight: document.documentElement.scrollHeight };
  });
  expect(dimensions.left).toBe(0);
  expect(Math.abs(dimensions.width - dimensions.viewportWidth)).toBeLessThanOrEqual(1);
  expect(Math.abs(dimensions.height - dimensions.viewportHeight)).toBeLessThanOrEqual(1);
  expect(dimensions.pageWidth).toBe(dimensions.viewportWidth);
  expect(dimensions.pageHeight).toBe(dimensions.viewportHeight);
});

test("active dashboard renews its session and CSRF token", async ({ page }) => {
  await page.goto("/dashboard/");
  await page.clock.install();
  const session = await page.evaluate(async () => {
    const response = await fetch("/v1/dashboard/session", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ username: "admin", password: "browser-test-password" }) });
    if (!response.ok) throw new Error(`login failed: ${response.status}`);
    const body = await response.json();
    sessionStorage.setItem("gideondb-dashboard-csrf", body.csrf_token);
    return body;
  });
  let renewals = 0;
  await page.route("**/v1/dashboard/session/refresh", async route => { renewals++; await route.continue(); });
  await page.goto("/dashboard/");
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
  await page.clock.fastForward(15 * 60 * 1000);
  await expect.poll(() => renewals).toBe(1);
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem("gideondb-dashboard-csrf"))).not.toBe(session.csrf_token);
});
