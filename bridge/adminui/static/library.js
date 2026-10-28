(() => {
  const form = document.getElementById("library-filter");
  if (!form) return;
  const ui = window.SwarmUI;
  const rows = document.getElementById("library-rows");
  const status = document.getElementById("library-status");
  const more = document.getElementById("library-more");
  const empty = document.getElementById("library-empty");
  const platform = document.getElementById("platform");
  const query = document.getElementById("library-query");
  const order = document.getElementById("library-sort");
  const table = document.getElementById("library-table");
  let offset = 0, hasMore = true, loading = false, generation = 0, controller;
  let filters, forceRefresh = new URLSearchParams(location.search).get("refresh") === "1";

  function row(item) {
    const tr = document.createElement("tr");
    const title = ui.element("td");
    title.append(ui.element("strong", item.name || item.fs_name));
    if (item.name && item.name !== item.fs_name) title.append(ui.element("small", item.fs_name));
    const action = ui.element("td", undefined, "actions");
    const link = ui.element("a", "Download");
    link.href = "/api/library/download?" + new URLSearchParams({id: item.id, name: item.fs_name});
    link.setAttribute("aria-label", "Download " + (item.name || item.fs_name));
    action.append(link);
    tr.append(title, ui.element("td", item.platform_slug), ui.element("td", item.size_human), action);
    return tr;
  }

  async function loadMore() {
    if (loading || !hasMore) return;
    loading = true;
    const version = generation;
    more.disabled = true;
    status.textContent = offset ? "Loading more games..." : "Loading your library. The first request may take longer.";
    status.className = "";
    table.setAttribute("aria-busy", "true");
    const params = new URLSearchParams(filters);
    params.set("offset", offset);
    params.set("limit", 100);
    if (offset === 0 && forceRefresh) params.set("refresh", "1");
    try {
      const data = await ui.request("/api/library/items?" + params, {signal: controller.signal});
      if (version !== generation) return;
      const fragment = document.createDocumentFragment();
      data.items.forEach(item => fragment.append(row(item)));
      rows.append(fragment);
      offset += data.items.length;
      hasMore = Boolean(data.has_more);
      const chosen = filters.get("platform") || "";
      platform.replaceChildren(ui.element("option", "All platforms"));
      platform.firstChild.value = "";
      const slugs = [...new Set([...(data.platforms || []), ...(chosen ? [chosen] : [])])].sort();
      slugs.forEach(slug => {
        const option = ui.element("option", slug);
        option.value = slug;
        platform.append(option);
      });
      platform.value = chosen;
      status.textContent = offset + " of " + data.total + " matching games shown (" + data.scanned + " in your library).";
      empty.hidden = data.total !== 0;
      more.hidden = !hasMore;
      more.textContent = "Load more";
      if (forceRefresh) {
        forceRefresh = false;
        const clean = new URL(location.href);
        clean.searchParams.delete("refresh");
        history.replaceState(null, "", clean);
      }
    } catch (error) {
      if (version !== generation || error.name === "AbortError") return;
      status.textContent = error.message;
      status.className = "status-bad";
      more.hidden = false;
      more.textContent = "Retry";
    } finally {
      if (version === generation) {
        loading = false;
        more.disabled = false;
        table.setAttribute("aria-busy", "false");
      }
    }
  }

  function reset(refresh) {
    generation++;
    if (controller) controller.abort();
    controller = new AbortController();
    filters = new URLSearchParams({q: query.value.trim(), platform: platform.value, sort: order.value || "name"});
    forceRefresh = refresh;
    offset = 0;
    hasMore = true;
    loading = false;
    rows.replaceChildren();
    empty.hidden = true;
    more.hidden = true;
    loadMore();
  }

  form.addEventListener("submit", event => {
    event.preventDefault();
    const params = new URLSearchParams({q: query.value.trim(), platform: platform.value, sort: order.value});
    history.pushState(null, "", "/library?" + params);
    reset(false);
  });
  document.getElementById("library-refresh").addEventListener("click", () => reset(true));
  more.addEventListener("click", loadMore);
  window.addEventListener("popstate", () => {
    const params = new URLSearchParams(location.search);
    query.value = params.get("q") || "";
    const slug = params.get("platform") || "";
    if (![...platform.options].some(option => option.value === slug)) {
      const option = ui.element("option", slug);
      option.value = slug;
      platform.append(option);
    }
    platform.value = slug;
    order.value = params.get("sort") || "name";
    reset(params.get("refresh") === "1");
  });
  reset(forceRefresh);
})();
