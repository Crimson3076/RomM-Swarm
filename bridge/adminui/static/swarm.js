var testSwarmButton = document.getElementById("test-swarm");
if (testSwarmButton) {
  testSwarmButton.addEventListener("click", async function () {
    var result = document.getElementById("test-swarm-result");
    result.textContent = "Testing...";
    try {
      var resp = await fetch("/api/swarm/test", { method: "POST" });
      var data = await resp.json();
      if (resp.ok) {
        result.textContent = "Connected (generation " + data.generation + ", " + data.outcome + ")";
        result.className = "status-ok";
      } else {
        result.textContent = data.error || "Connection failed";
        result.className = "status-bad";
      }
    } catch (e) {
      result.textContent = "Request failed: " + e;
      result.className = "status-bad";
    }
  });
}

var publishInventoryButton = document.getElementById("publish-inventory");
if (publishInventoryButton) {
  publishInventoryButton.addEventListener("click", async function () {
    var result = document.getElementById("publish-inventory-result");
    result.textContent = "Publishing...";
    try {
      var resp = await fetch("/api/swarm/publish-inventory", { method: "POST" });
      var data = await resp.json();
      if (resp.ok && data.published) {
        result.textContent = "Published revision " + data.revision + ": " +
          data.item_count + " item(s), " + data.distinct_files + " distinct file(s) swarm-wide";
        result.className = "status-ok";
      } else if (resp.ok) {
        var reasons = data.skip_reasons ? Object.keys(data.skip_reasons).map(function (k) {
          return k + " (" + data.skip_reasons[k] + ")";
        }).join(", ") : "";
        result.textContent = "Nothing published — " + (reasons || "no eligible holdings found");
        result.className = "status-bad";
      } else {
        result.textContent = data.error || "Publish failed";
        result.className = "status-bad";
      }
    } catch (e) {
      result.textContent = "Request failed: " + e;
      result.className = "status-bad";
    }
  });
}
