(function () {
  var table = document.getElementById("library-table");
  if (!table) return;

  var rows = document.getElementById("library-rows");
  var status = document.getElementById("library-status");
  var platform = table.dataset.platform || "";
  var offset = parseInt(table.dataset.offset, 10) || 0;
  var hasMore = table.dataset.hasMore === "true";
  var loading = false;

  function escapeHTML(s) {
    var div = document.createElement("div");
    div.textContent = s;
    return div.innerHTML;
  }

  function appendRow(item) {
    var tr = document.createElement("tr");
    tr.innerHTML =
      "<td>" + escapeHTML(item.fs_name) + "</td>" +
      "<td>" + escapeHTML(item.platform_slug) + "</td>" +
      "<td>" + escapeHTML(item.size_human) + "</td>" +
      "<td><a href=\"/api/library/download?id=" + encodeURIComponent(item.id) +
      "&name=" + encodeURIComponent(item.fs_name) + "\">Download</a></td>";
    rows.appendChild(tr);
  }

  function loadMore() {
    if (loading || !hasMore) return;
    loading = true;

    var url = "/api/library/items?offset=" + offset + "&limit=100";
    if (platform) url += "&platform=" + encodeURIComponent(platform);

    fetch(url)
      .then(function (resp) { return resp.json(); })
      .then(function (data) {
        (data.items || []).forEach(function (item) {
          appendRow(item);
          offset++;
        });
        hasMore = !!data.has_more;
        if (status) {
          status.textContent = offset + " item(s) shown" + (platform ? " on platform \"" + platform + "\"" : "") +
            (hasMore ? ", scroll for more…" : ".");
        }
        loading = false;
        maybeLoadMore();
      })
      .catch(function () {
        loading = false;
      });
  }

  // Loads more automatically while the table's bottom is already within
  // view (a short library that doesn't fill the viewport would otherwise
  // never trigger a scroll event at all), then falls back to the normal
  // near-the-bottom scroll trigger for everything past the first screen.
  function maybeLoadMore() {
    if (!hasMore || loading) return;
    var rect = table.getBoundingClientRect();
    if (rect.bottom <= window.innerHeight + 200) {
      loadMore();
    }
  }

  window.addEventListener("scroll", function () {
    if (window.innerHeight + window.scrollY >= document.body.offsetHeight - 300) {
      loadMore();
    }
  });

  maybeLoadMore();
})();
