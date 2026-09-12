(() => {
  document.querySelectorAll(".bridge-name-edit-toggle").forEach(button => {
    const display = document.getElementById(button.dataset.display);
    const form = document.getElementById(button.dataset.form);
    if (!display || !form) return;
    button.addEventListener("click", () => {
      display.hidden = true; button.hidden = true; form.hidden = false;
      form.querySelector("input").focus();
    });
    form.querySelector(".bridge-name-cancel")?.addEventListener("click", () => {
      form.hidden = true; display.hidden = false; button.hidden = false; form.reset(); button.focus();
    });
  });
  const deletion = document.getElementById("delete-swarm-form");
  if (deletion) {
    const input = document.getElementById("confirm_name");
    const button = document.getElementById("delete-swarm-button");
    const update = () => { button.disabled = input.value !== deletion.dataset.name; };
    input.addEventListener("input", update);
    update();
  }
  const query = document.getElementById("bridge-query");
  const filter = document.getElementById("bridge-filter");
  if (query && filter) {
    const rows = [...document.querySelectorAll("[data-bridge]")];
    const update = () => {
      let count = 0;
      rows.forEach(row => {
        const matches = row.dataset.search.toLowerCase().includes(query.value.toLowerCase()) &&
          (filter.value === "all" || row.dataset.credential === filter.value ||
           (filter.value === "unpublished" && row.dataset.published === "false"));
        row.hidden = !matches;
        if (matches) count++;
      });
      document.getElementById("bridge-count").textContent = count + " of " + rows.length + " Bridges shown.";
    };
    query.addEventListener("input", update);
    filter.addEventListener("change", update);
    update();
  }
  document.getElementById("copy-secret")?.addEventListener("click", async () => {
    const code = document.querySelector(".code-display");
    const result = document.getElementById("copy-status");
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(code.textContent);
      } else {
        const range = document.createRange();
        range.selectNodeContents(code);
        const selection = window.getSelection();
        selection.removeAllRanges();
        selection.addRange(range);
        if (!document.execCommand("copy")) throw new Error("Select the code and copy it manually.");
        selection.removeAllRanges();
      }
      result.textContent = "Copied.";
    } catch {
      result.textContent = "Select the code above and copy it manually.";
    }
  });
})();
