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

async function navigate(page, testInfo, name) {
  if (testInfo.project.name.startsWith("mobile")) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name, exact: true }).click();
}

async function createCollection(page, name, dimension = 3) {
  const created = await page.evaluate(async collection => {
    const response = await fetch("/v1/collections", { method: "POST", headers: { "Content-Type": "application/json", "X-GideonDB-CSRF": sessionStorage.getItem("gideondb-dashboard-csrf") }, body: JSON.stringify(collection) });
    return response.ok;
  }, { name, dimension, metric: "cosine", shard_count: 2 });
  expect(created).toBeTruthy();
}

test("collection detail shows configuration, shards, and imports a file", async ({ page }, testInfo) => {
  const name = `ops-${testInfo.project.name}-${Date.now()}`;
  await createCollection(page, name);
  await page.reload();
  await navigate(page, testInfo, "Collections");
  await page.getByRole("link", { name: `Open collection ${name}` }).click();
  await expect(page).toHaveURL(new RegExp(`#collection/${name}$`));
  await expect(page.locator("#page-title")).toHaveText(name);
  await expect(page.locator("#detail-dimension")).toHaveText("3");
  await expect(page.locator("#detail-shards")).toHaveText("2");
  await expect(page.locator("#detail-index")).toContainText("flat");
  await expect(page.locator("#detail-shard-body tr")).toHaveCount(2);

  await page.getByRole("button", { name: "Import data" }).first().click();
  const csv = "id,vector,category\nalpha,0.1 0.2 0.3,guide\nbeta,\"[0.3,0.2,0.1]\",api\nbroken,0.1 0.2,api\n";
  await page.getByLabel("File").setInputFiles({ name: "records.csv", mimeType: "text/csv", buffer: Buffer.from(csv) });
  await expect(page.locator("#import-valid")).toHaveText("2");
  await expect(page.locator("#import-invalid")).toHaveText("1");
  await expect(page.locator("#import-errors")).toContainText("expected 3 dimensions");
  await page.getByRole("button", { name: "Import 2 records" }).click();
  await expect(page.locator("#import-status")).toContainText("Imported 2 records");
  await page.getByRole("button", { name: "Close" }).click();
  await expect(page.locator("#detail-vectors")).toHaveText("2");
});

test("deep link opens collection detail and typed confirmation guards deletion", async ({ page }, testInfo) => {
  const name = `ops-delete-${testInfo.project.name}-${Date.now()}`;
  await createCollection(page, name);
  await page.goto(`/dashboard/#collection/${name}`);
  await expect(page.locator("#detail-name")).toHaveText(name);
  await page.locator("#detail-delete").click();
  const submit = page.getByRole("button", { name: "Delete collection" });
  await expect(submit).toBeDisabled();
  await page.getByLabel("Collection name").fill("wrong");
  await expect(submit).toBeDisabled();
  await page.getByLabel("Collection name").fill(name);
  await submit.click();
  await expect(page.locator("#page-title")).toHaveText("Collections");
  await expect(page.locator("#collections-grid")).not.toContainText(name);
});

test("cluster view shows replica placement and previews a rebalance plan", async ({ page }, testInfo) => {
  await createCollection(page, `ops-cluster-${testInfo.project.name}-${Date.now()}`);
  await navigate(page, testInfo, "Cluster");
  await expect(page.locator("#replica-factor")).toHaveText(/RF \d+/);
  await expect(page.locator("#replica-nodes .node-chip").first()).toContainText("(local)");
  await expect(page.locator("#replica-body tr").first()).toBeVisible();
  await expect(page.locator("#rebalance-prepare")).toBeDisabled();
  await page.getByRole("combobox", { name: "Change" }).selectOption("capacity");
  await page.getByLabel("Node ID").fill(await page.locator("#local-node").textContent());
  await page.getByLabel("Placement capacity").fill("2");
  await page.getByRole("button", { name: "Preview plan" }).click();
  await expect(page.locator("#rebalance-status")).toContainText("Plan ready");
  await expect(page.locator("#rebalance-prepare")).toBeEnabled();
  await expect(page.locator('#rebalance-steps li[data-step="prepare"]')).toHaveClass(/current/);
});

test("recovery point shows the manifest or the server's refusal", async ({ page }, testInfo) => {
  let succeed = false;
  await page.route("**/v1/cluster/backup/recovery-point", route => route.fulfill(succeed
    ? { status: 200, contentType: "application/json", body: JSON.stringify({ operation: "dashboard-test", recovery_point: { format: 1, metadata_epoch: 7, placement_digest: "abc123", nodes: ["node-a", "node-b"], shard_recovery: [{}, {}, {}] } }) }
    : { status: 503, contentType: "application/json", body: JSON.stringify({ code: "cluster_not_ready", message: "authoritative converged placement is required" }) }));
  await navigate(page, testInfo, "Resilience");
  const create = page.getByRole("button", { name: "Create recovery point" });
  await create.click();
  await expect(page.locator("#recovery-status")).toHaveClass(/error/);
  await expect(page.locator("#recovery-status")).toContainText("authoritative converged placement is required");
  await expect(page.locator("#recovery-result")).toBeHidden();
  succeed = true;
  await create.click();
  await expect(page.locator("#recovery-status")).toHaveClass(/ok/);
  await expect(page.locator("#recovery-details")).toContainText("abc123");
  await expect(page.locator("#recovery-details")).toContainText("3");
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download manifest" }).click();
  expect((await download).suggestedFilename()).toBe("dashboard-test.json");
});
