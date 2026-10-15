export async function setupAdmin({api, state, samplePerformance, showNotice}) {
  const $ = id => document.getElementById(id);
  let currentUser = "", editing = null, timer;
  const notice = (id, message) => { $(id).textContent = message; $(id).classList.toggle("hidden", !message); };
  function applySettings(settings) {
    $("admin-site-name").value = settings.site_name;
    $("admin-session-hours").value = settings.session_hours;
    $("admin-refresh-seconds").value = settings.refresh_seconds;
    document.querySelector(".brand strong").textContent = settings.site_name;
    document.title = `${settings.site_name} Console`;
    clearInterval(timer);
    timer = setInterval(samplePerformance, settings.refresh_seconds * 1000);
  }
  function editUser(user = null) {
    editing = user;
    $("user-form").reset();
    $("user-dialog-title").textContent = user ? `Edit ${user.username}` : "Add user";
    $("user-name").value = user?.username || "";
    $("user-name").disabled = !!user;
    $("user-role").value = user?.role || "reader";
    $("user-disabled").checked = user?.disabled || false;
    $("user-role").disabled = user?.username === currentUser;
    $("user-disabled").disabled = user?.username === currentUser;
    $("user-password").required = !user;
    $("user-password-help").textContent = user ? "Leave blank to keep the password, or enter at least 12 characters to reset it. Saving changes signs this user out." : "At least 12 characters. The user must replace it at sign-in.";
    notice("user-error", "");
    $("user-dialog").showModal();
  }
  async function loadUsers() {
    notice("users-status", "Loading users…");
    try {
      const {users} = await api("/v1/dashboard/users");
      $("users-body").replaceChildren();
      for (const user of users) {
        const row = document.createElement("tr");
        for (const [index, value] of [user.username + (user.username === currentUser ? " (you)" : ""), user.role, user.disabled ? "Disabled" : user.must_change_password ? "Password change required" : "Active"].entries()) {
          const cell = document.createElement("td"); cell.textContent = value; cell.dataset.label = ["Username", "Role", "Status"][index]; row.append(cell);
        }
        const actions = document.createElement("td"); actions.className = "user-actions"; actions.dataset.label = "Actions";
        const edit = document.createElement("button"); edit.className = "secondary"; edit.textContent = "Edit";
        edit.setAttribute("aria-label", `Edit ${user.username}`); edit.addEventListener("click", () => editUser(user)); actions.append(edit);
        const remove = document.createElement("button"); remove.className = "danger"; remove.textContent = "Delete";
        remove.setAttribute("aria-label", `Delete ${user.username}`); remove.disabled = user.username === currentUser;
        remove.addEventListener("click", async () => {
          if (!confirm(`Delete user ${user.username}? Their active sessions will end.`)) return;
          remove.disabled = true;
          try { await api(`/v1/dashboard/users/${encodeURIComponent(user.username)}`, {method:"DELETE"}); await loadUsers(); }
          catch (error) { notice("users-status", error.message); remove.disabled = false; }
        });
        actions.append(remove); row.append(actions); $("users-body").append(row);
      }
      notice("users-status", `${users.length} user${users.length === 1 ? "" : "s"}`);
    } catch (error) { notice("users-status", error.message); }
  }
  $("add-user").addEventListener("click", () => editUser());
  $("cancel-user").addEventListener("click", () => $("user-dialog").close());
  $("user-form").addEventListener("submit", async event => {
    event.preventDefault(); $("save-user").disabled = true; notice("user-error", "");
    try {
      const username = $("user-name").value;
      await api(editing ? `/v1/dashboard/users/${encodeURIComponent(username)}` : "/v1/dashboard/users", {
        method: editing ? "PUT" : "POST", headers: {"Content-Type":"application/json"},
        body: JSON.stringify({username, role:$("user-role").value, disabled:$("user-disabled").checked, password:$("user-password").value})
      });
      $("user-password").value = ""; $("user-dialog").close();
      if (editing?.username === currentUser) { sessionStorage.removeItem("gideondb-dashboard-csrf"); location.replace("/dashboard/"); return; }
      await loadUsers();
    } catch (error) { notice("user-error", error.message); }
    finally { $("save-user").disabled = false; }
  });
  $("admin-settings-form").addEventListener("submit", async event => {
    event.preventDefault(); const button = event.currentTarget.querySelector("button"); button.disabled = true;
    try {
      const settings = await api("/v1/dashboard/settings", {method:"PUT", headers:{"Content-Type":"application/json"}, body:JSON.stringify({site_name:$("admin-site-name").value, session_hours:Number($("admin-session-hours").value), refresh_seconds:Number($("admin-refresh-seconds").value)})});
      applySettings(settings); notice("admin-settings-status", "Settings saved.");
    } catch (error) { notice("admin-settings-status", error.message); }
    finally { button.disabled = false; }
  });
  $("users-nav").addEventListener("click", loadUsers);
  try {
    const session = await api("/v1/dashboard/session");
    currentUser = session.username;
    if (session.csrf_token) { state.csrf = session.csrf_token; sessionStorage.setItem("gideondb-dashboard-csrf", state.csrf); }
    const admin = session.role === "admin";
    $("users-nav").classList.toggle("hidden", !admin);
    $("admin-settings-panel").classList.toggle("hidden", !admin);
    if (!admin && location.hash === "#users") document.querySelector('[data-view="overview"]').click();
    applySettings(await api("/v1/dashboard/preferences"));
    if (admin && location.hash === "#users") await loadUsers();
  } catch (error) {
    timer = setInterval(samplePerformance, 5000);
    $("users-nav").classList.add("hidden"); $("admin-settings-panel").classList.add("hidden");
    showNotice(error.message);
  }
}
