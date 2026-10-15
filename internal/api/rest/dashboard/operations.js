// Cluster operations, recovery points, collection detail and bulk import.
// Every value from the server is rendered with textContent.
import { radixIcon } from "./radix-icons.js";
const IMPORT_BATCH_SIZE = 200;
const IMPORT_ERROR_LIMIT = 5;

export function setupOperations({api, state, element, showNotice, refresh, deepLink = ""}) {
  const $ = id => document.getElementById(id);
  let rebalanceRequest = null;
  let importRecords = [];
  let importCollection = "";
  let detailName = "";
  let lastRecoveryPoint = null;

  const setStatus = (id, message, tone = "") => {
    const node = $(id);
    node.textContent = message;
    node.className = `op-status${tone ? ` ${tone}` : ""}`;
    node.classList.toggle("hidden", !message);
  };
  const cell = (row, value, className) => { const td = element("td", className, value ?? "—"); row.append(td); return td; };
  const badge = (label, tone = "") => element("span", `badge${tone ? ` ${tone}` : ""}`, label);

  // ---------- Replica placement ----------
  async function loadReplicas() {
    const factor = Number(state.node.replication_factor) || 1;
    const body = $("replica-body");
    try {
      const table = await api(`/v1/cluster/replicas?replication_factor=${factor}`);
      state.replicaTable = table;
      $("replica-factor").textContent = `RF ${table.replication_factor}`;
      $("replica-authority").textContent = table.authoritative ? "Authoritative" : "Advisory";
      body.replaceChildren();
      const nodes = $("replica-nodes");
      nodes.replaceChildren();
      for (const node of table.nodes || []) {
        const chip = element("span", `node-chip${node.healthy ? "" : " unhealthy"}`);
        chip.append(element("span", "dot" + (node.healthy ? " online" : "")), element("span", null, `${node.node_id}${node.local ? " (local)" : ""}`), element("small", null, `capacity ${node.capacity}`));
        nodes.append(chip);
      }
      const shards = table.shards || [];
      if (!shards.length) {
        const row = element("tr"); const td = cell(row, "No collections are placed yet."); td.colSpan = 4; td.className = "muted"; body.append(row);
      }
      for (const shard of shards) {
        const row = element("tr");
        cell(row, shard.collection);
        cell(row, String(shard.shard_id), "mono");
        cell(row, shard.leader_id, "mono");
        const replicas = cell(row, "");
        replicas.className = "replica-list";
        for (const id of shard.replicas || []) replicas.append(badge(id === shard.leader_id ? `${id} · leader` : id, id === shard.leader_id ? "" : "neutral"));
        body.append(row);
      }
      setStatus("replica-status", "");
    } catch (error) {
      body.replaceChildren();
      setStatus("replica-status", `Replica placement unavailable: ${error.message}`, "warn");
    }
  }

  // ---------- Rebalance workflow ----------
  function syncRebalanceFields() {
    const kind = $("rebalance-kind").value;
    $("rebalance-address-field").classList.toggle("hidden", kind !== "join");
    $("rebalance-capacity-field").classList.toggle("hidden", kind !== "capacity");
    $("rebalance-address").required = kind === "join";
    $("rebalance-capacity").required = kind === "capacity";
  }
  function rebalanceParams() {
    const kind = $("rebalance-kind").value, node = $("rebalance-node").value.trim();
    if (kind === "join") return {join_node_id: node, join_address: $("rebalance-address").value.trim()};
    if (kind === "leave") return {leave_node_id: node};
    return {capacity_node_id: node, placement_capacity: Number($("rebalance-capacity").value)};
  }
  function setRebalanceStage(stage) {
    // stage: idle | planned | prepared
    $("rebalance-prepare").disabled = stage !== "planned";
    $("rebalance-apply").disabled = stage !== "prepared";
    $("rebalance-abort").disabled = stage === "idle";
    document.querySelectorAll("#rebalance-steps li").forEach(item => {
      const order = ["plan", "prepare", "apply"], index = order.indexOf(item.dataset.step);
      const reached = {idle: -1, planned: 0, prepared: 1, applied: 2}[stage];
      item.classList.toggle("done", index <= reached);
      item.classList.toggle("current", index === reached + 1);
    });
  }
  function renderPlan(plan) {
    const root = $("rebalance-plan");
    root.replaceChildren();
    const movements = plan?.movements || [];
    $("rebalance-summary").textContent = movements.length ? `${movements.length} shard movement${movements.length === 1 ? "" : "s"} · epoch ${plan.from_epoch} → ${plan.to_epoch}` : "No shard movement is required for this change.";
    for (const move of movements) {
      const row = element("tr");
      cell(row, `${move.collection} / ${move.shard_id}`);
      cell(row, `${move.source_leader_id} → ${move.target_leader_id}`, "mono");
      cell(row, (move.target_replicas || []).join(", "), "mono");
      cell(row, String((move.actions || []).length));
      root.append(row);
    }
  }
  async function rebalanceCall(path, label) {
    const node = await api("/v1/node");
    return api(path, {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({...rebalanceRequest, expected_epoch: node.metadata_epoch})});
  }
  $("rebalance-kind").addEventListener("change", syncRebalanceFields);
  $("rebalance-form").addEventListener("submit", async event => {
    event.preventDefault();
    const params = rebalanceParams();
    const query = new URLSearchParams(Object.entries(params).map(([key, value]) => [key, String(value)]));
    setStatus("rebalance-status", "Computing plan…");
    try {
      const result = await api(`/v1/cluster/rebalance/plan?${query}`);
      rebalanceRequest = params;
      renderPlan(result.plan);
      $("rebalance-result").classList.remove("hidden");
      setRebalanceStage("planned");
      setStatus("rebalance-status", "Plan ready. Review the movements, then prepare data.", "ok");
    } catch (error) {
      rebalanceRequest = null;
      $("rebalance-result").classList.add("hidden");
      setRebalanceStage("idle");
      setStatus("rebalance-status", error.message, "error");
    }
  });
  $("rebalance-prepare").addEventListener("click", async () => {
    if (!confirm("Prepare this rebalance? New replicas will be copied and caught up before cutover.")) return;
    setStatus("rebalance-status", "Preparing data on target replicas… this can take up to 30 seconds.");
    try {
      await rebalanceCall("/v1/cluster/rebalance/prepare");
      setRebalanceStage("prepared");
      setStatus("rebalance-status", "Data preparation complete. Apply the cutover to commit the new placement.", "ok");
    } catch (error) { setStatus("rebalance-status", error.message, "error"); }
  });
  $("rebalance-apply").addEventListener("click", async () => {
    if (!confirm("Apply the membership cutover? This commits a new metadata epoch.")) return;
    setStatus("rebalance-status", "Applying cutover…");
    try {
      const result = await rebalanceCall("/v1/cluster/rebalance/apply");
      setRebalanceStage("applied");
      $("rebalance-apply").disabled = true; $("rebalance-abort").disabled = true; $("rebalance-prepare").disabled = true;
      setStatus("rebalance-status", `Rebalance applied${result?.metadata_epoch ? ` at epoch ${result.metadata_epoch}` : ""}.`, "ok");
      rebalanceRequest = null;
      refresh();
    } catch (error) { setStatus("rebalance-status", error.message, "error"); }
  });
  $("rebalance-abort").addEventListener("click", async () => {
    if (!confirm("Abort this rebalance and release source barriers?")) return;
    setStatus("rebalance-status", "Aborting…");
    try {
      await rebalanceCall("/v1/cluster/rebalance/abort");
      rebalanceRequest = null;
      setRebalanceStage("idle");
      $("rebalance-result").classList.add("hidden");
      setStatus("rebalance-status", "Rebalance aborted.", "ok");
    } catch (error) { setStatus("rebalance-status", error.message, "error"); }
  });
  syncRebalanceFields();
  setRebalanceStage("idle");

  // ---------- Recovery point ----------
  $("create-recovery-point").addEventListener("click", async () => {
    const button = $("create-recovery-point");
    button.disabled = true;
    setStatus("recovery-status", "Freezing writes and capturing a coordinated recovery point…");
    try {
      const operation = `dashboard-${new Date().toISOString().replace(/[:.]/g, "-")}`;
      const result = await api("/v1/cluster/backup/recovery-point", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({operation})});
      lastRecoveryPoint = result;
      const point = result.recovery_point || {};
      const details = $("recovery-details");
      details.replaceChildren();
      for (const [label, value] of [["Operation", result.operation], ["Metadata epoch", point.metadata_epoch], ["Nodes", (point.nodes || []).length], ["Shards captured", (point.shard_recovery || []).length], ["Placement digest", point.placement_digest]]) {
        const box = element("div"); box.append(element("dt", null, label), element("dd", null, String(value ?? "—"))); details.append(box);
      }
      $("recovery-result").classList.remove("hidden");
      setStatus("recovery-status", "Recovery point captured. Download the manifest and store it with your snapshot.", "ok");
    } catch (error) {
      setStatus("recovery-status", `Recovery point failed: ${error.message}`, "error");
    } finally { button.disabled = false; }
  });
  $("download-recovery-point").addEventListener("click", () => {
    if (!lastRecoveryPoint) return;
    const blob = new Blob([JSON.stringify(lastRecoveryPoint, null, 2)], {type: "application/json"});
    const link = element("a"); link.href = URL.createObjectURL(blob); link.download = `${lastRecoveryPoint.operation}.json`;
    link.click(); setTimeout(() => URL.revokeObjectURL(link.href), 1000);
  });

  // ---------- Collection detail ----------
  function showView(id, title, context) {
    document.querySelectorAll(".view").forEach(view => view.classList.add("hidden"));
    $(id).classList.remove("hidden");
    $("page-title").textContent = title;
    $("page-context").textContent = context;
  }
  async function openCollection(name) {
    detailName = name;
    document.querySelectorAll(".nav").forEach(nav => nav.classList.toggle("active", nav.dataset.view === "collections"));
    showView("view-collection", name, "CATALOG");
    history.replaceState(null, "", `#collection/${encodeURIComponent(name)}`);
    $("detail-name").textContent = name;
    setStatus("detail-status", "Loading collection…");
    try {
      const {config, vector_count: count} = await api(`/v1/collections/${encodeURIComponent(name)}`);
      $("detail-summary").textContent = `${config.dimension} dimensions · ${config.metric} · ${config.index?.type || "flat"} index`;
      $("detail-vectors").textContent = Number(count ?? 0).toLocaleString();
      $("detail-dimension").textContent = config.dimension;
      $("detail-shards").textContent = config.shard_count;
      $("detail-metric").textContent = config.metric;
      const index = $("detail-index");
      index.replaceChildren();
      for (const [key, value] of Object.entries({...(config.index || {}), type: config.index?.type || "flat"}).filter(([, value]) => value !== "" && value !== null && value !== undefined)) {
        const box = element("div"); box.append(element("dt", null, key.replaceAll("_", " ")), element("dd", null, String(value))); index.append(box);
      }
      setStatus("detail-status", "");
    } catch (error) {
      setStatus("detail-status", error.message, "error");
    }
    if (!state.replicaTable) await loadReplicas();
    const shards = (state.replicaTable?.shards || []).filter(shard => shard.collection === name);
    const body = $("detail-shard-body");
    body.replaceChildren();
    if (!shards.length) { const row = element("tr"); const td = cell(row, "Shard placement is not available on this node."); td.colSpan = 3; td.className = "muted"; body.append(row); }
    for (const shard of shards) {
      const row = element("tr");
      cell(row, String(shard.shard_id), "mono"); cell(row, shard.leader_id, "mono"); cell(row, (shard.replicas || []).join(", "), "mono");
      body.append(row);
    }
  }
  $("collections-grid").addEventListener("click", event => {
    if (event.target.closest("button")) return;
    const card = event.target.closest(".collection");
    const name = card?.querySelector("h3")?.textContent;
    if (name) openCollection(name);
  });
  $("collections-grid").addEventListener("keydown", event => {
    if (event.key !== "Enter" || event.target.closest("button")) return;
    const name = event.target.closest(".collection")?.querySelector("h3")?.textContent;
    if (name) openCollection(name);
  });
  new MutationObserver(() => document.querySelectorAll("#collections-grid .collection").forEach(card => {
    if (card.tabIndex === 0) return;
    card.tabIndex = 0; card.setAttribute("role", "link");
    card.setAttribute("aria-label", `Open collection ${card.querySelector("h3")?.textContent}`);
  })).observe($("collections-grid"), {childList: true});
  $("detail-back").addEventListener("click", () => document.querySelector('.nav[data-view="collections"]').click());
  $("detail-browse").addEventListener("click", () => {
    document.querySelector('.nav[data-view="explorer"]').click();
    $("explorer-collection").value = detailName;
    $("browse-records").click();
  });
  $("detail-import").addEventListener("click", () => openImport(detailName));
  $("detail-delete").addEventListener("click", () => {
    $("delete-confirm-name").textContent = detailName;
    $("delete-confirm-input").value = "";
    $("delete-confirm-submit").disabled = true;
    $("delete-dialog").showModal();
  });
  $("delete-confirm-input").addEventListener("input", () => { $("delete-confirm-submit").disabled = $("delete-confirm-input").value !== detailName; });
  $("delete-cancel").addEventListener("click", () => $("delete-dialog").close());
  $("delete-form").addEventListener("submit", async event => {
    event.preventDefault();
    try {
      await api(`/v1/collections/${encodeURIComponent(detailName)}`, {method: "DELETE"});
      $("delete-dialog").close();
      showNotice(`Collection ${detailName} deleted.`);
      document.querySelector('.nav[data-view="collections"]').click();
      refresh();
    } catch (error) { $("delete-dialog").close(); setStatus("detail-status", error.message, "error"); }
  });
  $("refresh").addEventListener("click", () => {
    state.replicaTable = null;
    if (!$("view-cluster").classList.contains("hidden")) loadReplicas();
    if (!$("view-collection").classList.contains("hidden")) openCollection(detailName);
  });
  document.querySelector('.nav[data-view="cluster"]').addEventListener("click", loadReplicas);

  // ---------- Bulk import ----------
  function openImport(collection) {
    importCollection = collection || $("explorer-collection").value || state.collections[0]?.name || "";
    importRecords = [];
    $("import-form").reset();
    $("import-target").textContent = importCollection || "No collection selected";
    $("import-preview").classList.add("hidden");
    $("import-progress-wrap").classList.add("hidden");
    $("import-submit").disabled = true;
    setStatus("import-status", importCollection ? "" : "Create a collection before importing data.", importCollection ? "" : "warn");
    $("import-dialog").showModal();
  }
  function parseCSV(source) {
    const rows = []; let row = [], field = "", quoted = false;
    for (let i = 0; i < source.length; i++) {
      const char = source[i];
      if (quoted) {
        if (char === '"' && source[i + 1] === '"') { field += '"'; i++; }
        else if (char === '"') quoted = false;
        else field += char;
      } else if (char === '"') quoted = true;
      else if (char === ",") { row.push(field); field = ""; }
      else if (char === "\n" || char === "\r") {
        if (char === "\r" && source[i + 1] === "\n") i++;
        row.push(field); field = ""; if (row.some(value => value !== "")) rows.push(row); row = [];
      } else field += char;
    }
    row.push(field); if (row.some(value => value !== "")) rows.push(row);
    const [header, ...data] = rows;
    if (!header) return [];
    const columns = header.map(name => name.trim().toLowerCase());
    if (!columns.includes("id") || !columns.includes("vector")) throw new Error("CSV needs at least \"id\" and \"vector\" columns");
    return data.map(values => {
      const record = {}, metadata = {};
      columns.forEach((column, index) => {
        const value = (values[index] ?? "").trim();
        if (column === "id" || column === "namespace") { if (value) record[column] = value; }
        else if (column === "vector") record.vector = value.startsWith("[") ? JSON.parse(value) : value.split(/[\s;|]+/).filter(Boolean).map(Number);
        else if (column === "metadata" || column === "payload") { if (value) record[column] = JSON.parse(value); }
        else if (value !== "") metadata[column] = Number.isFinite(Number(value)) && value !== "" ? Number(value) : value;
      });
      if (Object.keys(metadata).length) record.metadata = {...metadata, ...(record.metadata || {})};
      return record;
    });
  }
  function parseImport(name, source) {
    const lower = name.toLowerCase();
    if (lower.endsWith(".csv")) return parseCSV(source);
    if (lower.endsWith(".jsonl") || lower.endsWith(".ndjson")) return source.split(/\r?\n/).filter(line => line.trim()).map((line, index) => {
      try { return JSON.parse(line); } catch { throw new Error(`Line ${index + 1} is not valid JSON`); }
    });
    const parsed = JSON.parse(source);
    const records = Array.isArray(parsed) ? parsed : parsed?.records;
    if (!Array.isArray(records)) throw new Error("JSON must be an array of records or an object with a \"records\" array");
    return records;
  }
  function validate(records, dimension) {
    const valid = [], errors = [];
    records.forEach((record, index) => {
      const where = `Record ${index + 1}`;
      if (!record || typeof record !== "object" || Array.isArray(record)) return errors.push(`${where}: not an object`);
      if (typeof record.id !== "string" && typeof record.id !== "number" || String(record.id).trim() === "") return errors.push(`${where}: missing id`);
      if (!Array.isArray(record.vector) || record.vector.some(value => typeof value !== "number" || !Number.isFinite(value))) return errors.push(`${where} (${record.id}): vector must be an array of numbers`);
      if (dimension && record.vector.length !== dimension) return errors.push(`${where} (${record.id}): expected ${dimension} dimensions, got ${record.vector.length}`);
      const clean = {id: String(record.id), vector: record.vector};
      for (const key of ["namespace", "metadata", "payload", "sparse_vector"]) if (record[key] !== undefined) clean[key] = record[key];
      valid.push(clean);
    });
    return {valid, errors};
  }
  $("import-file").addEventListener("change", async () => {
    const file = $("import-file").files[0];
    importRecords = [];
    $("import-submit").disabled = true;
    $("import-preview").classList.add("hidden");
    if (!file) return;
    setStatus("import-status", `Reading ${file.name}…`);
    try {
      const collection = state.collections.find(item => item.name === importCollection);
      const {valid, errors} = validate(parseImport(file.name, await file.text()), collection?.dimension);
      importRecords = valid;
      $("import-valid").textContent = valid.length.toLocaleString();
      $("import-invalid").textContent = errors.length.toLocaleString();
      $("import-batches").textContent = Math.ceil(valid.length / IMPORT_BATCH_SIZE).toLocaleString();
      const list = $("import-errors");
      list.replaceChildren(...errors.slice(0, IMPORT_ERROR_LIMIT).map(message => element("li", null, message)));
      if (errors.length > IMPORT_ERROR_LIMIT) list.append(element("li", "muted", `…and ${errors.length - IMPORT_ERROR_LIMIT} more`));
      list.classList.toggle("hidden", !errors.length);
      $("import-preview").classList.remove("hidden");
      $("import-submit").disabled = !valid.length || !importCollection;
      $("import-submit").textContent = `Import ${valid.length.toLocaleString()} record${valid.length === 1 ? "" : "s"}`;
      setStatus("import-status", errors.length ? "Invalid records will be skipped." : "", errors.length ? "warn" : "");
    } catch (error) {
      setStatus("import-status", `Could not read file: ${error.message}`, "error");
    }
  });
  $("import-form").addEventListener("submit", async event => {
    event.preventDefault();
    const namespace = $("import-namespace").value.trim();
    const records = namespace ? importRecords.map(record => ({...record, namespace})) : importRecords;
    const total = records.length, progress = $("import-progress");
    let done = 0;
    $("import-submit").disabled = true; $("import-file").disabled = true;
    $("import-progress-wrap").classList.remove("hidden");
    progress.max = total; progress.value = 0;
    try {
      for (let start = 0; start < total; start += IMPORT_BATCH_SIZE) {
        const batch = records.slice(start, start + IMPORT_BATCH_SIZE);
        await api(`/v1/collections/${encodeURIComponent(importCollection)}/vectors/batch`, {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({records: batch})});
        done += batch.length;
        progress.value = done;
        $("import-progress-label").textContent = `${done.toLocaleString()} of ${total.toLocaleString()} records`;
      }
      setStatus("import-status", `Imported ${done.toLocaleString()} records into ${importCollection}.`, "ok");
      refresh();
      if (!$("view-collection").classList.contains("hidden")) openCollection(importCollection);
    } catch (error) {
      setStatus("import-status", `Stopped after ${done.toLocaleString()} records: ${error.message}`, "error");
    } finally { $("import-file").disabled = false; }
  });
  $("import-cancel").addEventListener("click", () => $("import-dialog").close());
  $("explorer-import").addEventListener("click", () => openImport($("explorer-collection").value));

  // Leading icons on primary actions (decorative; accessible names unchanged).
  for (const [id, icon] of [["show-create", "plus"], ["show-insert", "plus"], ["explorer-import", "upload"], ["detail-import", "upload"], ["detail-browse", "table"], ["detail-delete", "trash"], ["browse-records", "play"], ["lookup-record", "search"], ["create-recovery-point", "layers"], ["download-recovery-point", "download"], ["check-backup-readiness", "refresh"], ["add-user", "plus"]]) {
    const button = $(id);
    if (button && !button.querySelector("svg")) button.prepend(radixIcon(icon));
  }

  // Deep link: #collection/<name>
  const openFromHash = hash => {
    const match = hash.match(/^#collection\/(.+)$/);
    if (match) openCollection(decodeURIComponent(match[1]));
  };
  addEventListener("hashchange", () => openFromHash(location.hash));
  openFromHash(deepLink);
  return {openCollection, loadReplicas};
}
