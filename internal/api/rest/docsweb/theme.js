const themeKey = "gideondb-theme";
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
    button.querySelector("[data-theme-icon]").textContent = value === "dark" ? "☀" : "☾";
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
