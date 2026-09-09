const themeKey = "gideondb-theme";
const radixThemeIcon = name => `<svg class="radix-icon" viewBox="0 0 15 15" aria-hidden="true"><path d="${name === "sun" ? "M7 0h1v2H7V0Zm0 13h1v2H7v-2ZM0 7h2v1H0V7Zm13 0h2v1h-2V7ZM7.5 3.5a4 4 0 1 1 0 8 4 4 0 0 1 0-8Zm0 1a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z" : "M9.4 1.2A6 6 0 1 0 13.8 9a5 5 0 0 1-4.4-7.8ZM7.5 2c.15 0 .3 0 .45.02A6 6 0 0 0 12.98 10 5 5 0 1 1-5.48-8Z"}"/></svg>`;
const systemTheme = () => matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";

function savedTheme() {
  try {
    const value = localStorage.getItem(themeKey);
    return value === "light" || value === "dark" ? value : null;
  } catch {
    return null;
  }
}

function applyTheme(theme) {
  const value = theme === "light" ? "light" : "dark";
  document.documentElement.dataset.theme = value;
  document.documentElement.style.colorScheme = value;
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", value === "light" ? "#f5f7f9" : "#090d12");
  document.querySelectorAll("[data-theme-toggle]").forEach(button => {
    const next = value === "dark" ? "light" : "dark";
    button.setAttribute("aria-label", `Switch to ${next} theme`);
    button.title = `Switch to ${next} theme`;
    button.querySelector("[data-theme-icon]").innerHTML = radixThemeIcon(value === "dark" ? "sun" : "moon");
    button.querySelector("[data-theme-label]").textContent = value === "dark" ? "Light" : "Dark";
  });
}

applyTheme(savedTheme() || systemTheme());

document.addEventListener("DOMContentLoaded", () => {
  applyTheme(document.documentElement.dataset.theme);
  document.querySelectorAll("[data-theme-toggle]").forEach(button => button.addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    try { localStorage.setItem(themeKey, next); } catch { /* The active page can still change theme. */ }
    applyTheme(next);
  }));
});

addEventListener("storage", event => {
  if (event.key === themeKey && (event.newValue === "light" || event.newValue === "dark")) applyTheme(event.newValue);
});
