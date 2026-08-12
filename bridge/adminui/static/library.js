(function () {
  var table = document.getElementById("library-table");
  if (!table) return;

  var rows = document.getElementById("library-rows");
  var status = document.getElementById("library-status");
  var platform = table.dataset.platform || "";
  var refresh = /[?&]refresh=1(&|$)/.test(window.location.search);
  var offset = 0;
  var hasMore = true;
  var loading = false;
  var firstLoad = true;

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

  function setStatus(total, scanned) {
    if (offset === 0) {
      status.textContent = "No items" + (platform ? " on platform \"" + platform + "\"" : "") +
        ", out of " + scanned + " scanned.";
      return;
    }
    status.textContent = offset + " of " + total + " item(s) shown" +
      (platform ? " on platform \"" + platform + "\"" : "") + ", out of " + scanned + " scanned" +
      (hasMore ? ", scroll for more…" : ".");
  }

  function loadMore() {
    if (loading || !hasMore) return;
    loading = true;
    if (firstLoad) {
      status.textContent = "Loading your library… this can take a while the first time; cached after that.";
    }

    var url = "/api/library/items?offset=" + offset + "&limit=100";
    if (platform) url += "&platform=" + encodeURIComponent(platform);
    if (firstLoad && refresh) url += "&refresh=1";

    fetch(url)
      .then(function (resp) {
        if (!resp.ok) throw new Error("request failed");
        return resp.json();
      })
      .then(function (data) {
        (data.items || []).forEach(function (item) {
          appendRow(item);
          offset++;
        });
        hasMore = !!data.has_more;
        firstLoad = false;
        loading = false;
        setStatus(data.total || 0, data.scanned || 0);
        maybeLoadMore();
      })
      .catch(function () {
        loading = false;
        if (firstLoad) {
          status.textContent = "Could not load your library. Try reloading the page.";
        }
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

  loadMore();
})();
