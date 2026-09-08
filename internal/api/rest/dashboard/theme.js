const themeKey = "gideondb-theme";
const systemTheme = matchMedia("(prefers-color-scheme: light)");

function readStoredTheme() {
  try {
    const value = localStorage.getItem(themeKey);
    return value === "light" || value === "dark" ? value : null;
  } catch {
    return null;
  }
}

function storeTheme(theme) {
  try { localStorage.setItem(themeKey, theme); } catch { /* Theme still applies for this page. */ }
}

function applyTheme(theme) {
  const normalized = theme === "light" ? "light" : "dark";
  const next = normalized === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = normalized;
  document.documentElement.style.colorScheme = normalized;
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.content = normalized === "dark" ? "#15191d" : "#f4f6f9";
  document.querySelectorAll("[data-theme-toggle]").forEach(button => {
    button.dataset.currentTheme = normalized;
    button.setAttribute("aria-label", `Switch to ${next} theme`);
    button.setAttribute("aria-pressed", String(normalized === "dark"));
    button.title = `Switch to ${next} theme`;
    const icon = button.querySelector("[data-theme-icon]");
    const label = button.querySelector("[data-theme-label]");
    if (icon) icon.textContent = normalized === "dark" ? "☀" : "☾";
    if (label) label.textContent = normalized === "dark" ? "Light" : "Dark";
  });
}

applyTheme(readStoredTheme() || (systemTheme.matches ? "light" : "dark"));

addEventListener("DOMContentLoaded", () => {
  applyTheme(document.documentElement.dataset.theme);
  document.querySelectorAll("[data-theme-toggle]").forEach(button => button.addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    storeTheme(next);
    applyTheme(next);
  }));
});

systemTheme.addEventListener("change", event => {
  if (!readStoredTheme()) applyTheme(event.matches ? "light" : "dark");
});

addEventListener("storage", event => {
  if (event.key === themeKey && (event.newValue === "light" || event.newValue === "dark")) applyTheme(event.newValue);
});
