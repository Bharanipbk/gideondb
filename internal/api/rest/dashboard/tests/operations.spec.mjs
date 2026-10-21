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

test("binary-quantized collection, text records, and hybrid search", async ({ page }, testInfo) => {
  const name = `hybrid-${testInfo.project.name}-${Date.now()}`;
  await navigate(page, testInfo, "Collections");
  await page.getByRole("button", { name: "Create collection" }).click();
  const dialog = page.locator("#create-dialog");
  await dialog.getByLabel("Name").fill(name);
  await dialog.getByLabel("Dimensions").fill("3");
  await dialog.getByLabel("Index type").selectOption("hnsw");
  await expect(dialog.locator('#create-quantization option[value="binary"]')).toHaveAttribute("disabled", "");
  await dialog.getByLabel("Index type").selectOption("flat");
  await expect(dialog.getByLabel("Oversampling")).toBeHidden();
  await dialog.getByLabel("Quantization").selectOption("binary");
  await dialog.getByLabel("Oversampling").fill("8");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog).toBeHidden();
  await page.getByRole("link", { name: `Open collection ${name}` }).click();
  await expect(page.locator("#detail-index")).toContainText("binary");
  await expect(page.locator("#detail-index")).toContainText("8");

  await navigate(page, testInfo, "Data explorer");
  await page.locator("#explorer-collection").selectOption(name);
  for (const [id, vector, text] of [["doc-a", "1, 0, 0", "gideon vector database"], ["doc-b", "0, 1, 0", "unrelated cooking recipe"]]) {
    await page.getByRole("button", { name: "Insert record" }).click();
    const insert = page.locator("#insert-dialog");
    await insert.getByLabel("ID").fill(id);
    await insert.getByLabel("Vector").fill(vector);
    await insert.getByLabel("Text").fill(text);
    await insert.getByRole("button", { name: "Upsert" }).click();
    await expect(insert).toBeHidden();
  }

  await navigate(page, testInfo, "Search playground");
  await page.locator("#search-collection").selectOption(name);
  await page.getByLabel("Text query").fill("gideon database");
  let sent = page.waitForRequest(request => request.url().includes("/search"));
  await page.getByRole("button", { name: "Run search" }).click();
  let body = (await sent).postDataJSON();
  expect(body.vector).toBeUndefined();
  expect(body.text).toBe("gideon database");
  await expect(page.locator("#search-results .result").first()).toContainText("doc-a");

  await page.locator("#search-form").getByLabel("Vector", { exact: true }).fill("0, 1, 0");
  await page.getByLabel("Fusion").selectOption("rrf");
  await page.getByLabel("Alpha (dense weight)").fill("0.3");
  sent = page.waitForRequest(request => request.url().includes("/search"));
  await page.getByRole("button", { name: "Run search" }).click();
  body = (await sent).postDataJSON();
  expect(body).toMatchObject({ fusion: "rrf", alpha: 0.3, text: "gideon database", vector: [0, 1, 0] });
  await expect(page.locator("#search-results .result")).toHaveCount(2);
  const deleted = await page.evaluate(async collection => (await fetch(`/v1/collections/${encodeURIComponent(collection)}`, { method: "DELETE", headers: { "X-GideonDB-CSRF": sessionStorage.getItem("gideondb-dashboard-csrf") } })).ok, name);
  expect(deleted).toBeTruthy();
});
