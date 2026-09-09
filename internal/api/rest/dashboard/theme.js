const themeKey = "gideondb-theme";
const radixThemeIcon = name => `<svg class="radix-icon" viewBox="0 0 15 15" aria-hidden="true"><path d="${name === "sun" ? "M7 0h1v2H7V0Zm0 13h1v2H7v-2ZM0 7h2v1H0V7Zm13 0h2v1h-2V7ZM2.2 1.5l1.4 1.4-.7.7-1.4-1.4.7-.7Zm9.9 9.9 1.4 1.4-.7.7-1.4-1.4.7-.7Zm.7-9.9.7.7-1.4 1.4-.7-.7 1.4-1.4ZM2.9 11.4l.7.7-1.4 1.4-.7-.7 1.4-1.4ZM7.5 3.5a4 4 0 1 1 0 8 4 4 0 0 1 0-8Zm0 1a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z" : "M9.4 1.2A6 6 0 1 0 13.8 9a5 5 0 0 1-4.4-7.8ZM7.5 2c.15 0 .3 0 .45.02A6 6 0 0 0 12.98 10 5 5 0 1 1-5.48-8Z"}"/></svg>`;
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
    if (icon) icon.innerHTML = radixThemeIcon(normalized === "dark" ? "sun" : "moon");
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
