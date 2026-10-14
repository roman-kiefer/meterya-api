// Authentication pages always follow the operating system's appearance.
(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
        document.documentElement.dataset.appearance = media.matches ? "dark" : "light";
    };
    apply();
    media.addEventListener("change", apply);
})();
