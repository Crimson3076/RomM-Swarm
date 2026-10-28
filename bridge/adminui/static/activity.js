(() => {
  const root = document.getElementById("activity-view");
  if (!root) return;
  const ui = window.SwarmUI, id = root.dataset.id;
  const status = document.getElementById("activity-status");
  const filter = document.getElementById("activity-filter");
  const query = document.getElementById("activity-query");
  let timer, busy = false, records = [], finished = false;
  const labels = {
    receiving: "Receiving", verified_in_staging: "Verified in staging",
    uploading_to_romm: "Uploading to RomM", published_to_filesystem: "Published to filesystem",
    awaiting_romm_ingestion: "Waiting for RomM", romm_matched: "Matched by RomM",
    source_active: "Complete", cancelled: "Cancelled", verification_conflict: "Verification conflict",
    romm_unmatched: "RomM match needs review", ingestion_timeout: "RomM indexing timed out",
    quarantined: "Quarantined"
  };
  if (filter) filter.value = new URLSearchParams(location.search).get("filter") || "all";

  function renderRows() {
    const body = document.getElementById("activity-rows");
    if (!body) return;
    const selected = records.filter(item => {
      const matches = !query.value || (item.id + " " + item.detail).toLowerCase().includes(query.value.toLowerCase());
      return matches && (!filter.value || filter.value === "all" ||
        (filter.value === "active" && item.running) ||
        (filter.value === "review" && item.tone === "bad") ||
        (filter.value === "complete" && item.state === "source_active"));
    });
    const fragment = document.createDocumentFragment();
    selected.forEach(item => {
      const tr = ui.element("tr"), title = ui.element("td"), state = ui.element("td");
      const link = ui.element("a", item.id);
      link.href = "/activity?" + new URLSearchParams({id: item.id});
      title.append(link, ui.element("small", item.detail));
      state.append(ui.element("span", item.label, "badge " + item.tone));
      tr.append(title, state, ui.element("td", ui.time(item.updated_at)));
      fragment.append(tr);
    });
    if (!selected.length) {
      const tr = ui.element("tr"), td = ui.element("td", records.length ? "No transfers match these filters." : "No transfers yet.");
      td.colSpan = 3; tr.append(td); fragment.append(tr);
    }
    body.replaceChildren(fragment);
  }

  async function update() {
    clearTimeout(timer);
    if (busy || document.hidden) return;
    busy = true;
    try {
      const data = await ui.request("/api/activity" + (id ? "?id=" + encodeURIComponent(id) : ""));
      if (id) {
        const fragment = document.createDocumentFragment();
        data.history.forEach(item => {
          const li = ui.element("li");
          li.append(ui.element("strong", labels[item.To] || item.To), ui.element("time", ui.time(item.At)), ui.element("p", item.Detail));
          fragment.append(li);
        });
        if (!data.history.length) fragment.append(ui.element("li", "Waiting for the first recorded update..."));
        document.getElementById("activity-history").replaceChildren(fragment);
        finished = !data.running;
      } else {
        records = data.items;
        renderRows();
      }
      status.textContent = (finished ? "Final recorded state. " : "Updated ") + new Date().toLocaleTimeString();
      status.className = "";
    } catch (error) {
      status.textContent = error.message + " Retrying while this tab is visible.";
      status.className = "status-bad";
    } finally {
      busy = false;
      if (!finished && !document.hidden) timer = setTimeout(update, 5000);
    }
  }
  filter?.addEventListener("change", () => {
    history.replaceState(null, "", "/activity?" + new URLSearchParams({filter: filter.value}));
    renderRows();
  });
  query?.addEventListener("input", renderRows);
  document.getElementById("activity-refresh").addEventListener("click", update);
  document.addEventListener("visibilitychange", () => { if (document.hidden) clearTimeout(timer); else update(); });
  update();
})();
