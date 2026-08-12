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

// Publish status is server-side state (Daemon.PublishStatus), not
// something this script owns — it persists across a reload on its own.
// This script's job is just to reflect it: render whatever the server
// last told it, and poll while a publish is actually running so the page
// updates live without the operator needing to reload.
var publishStatusDiv = document.getElementById("publish-status");
var publishStatusText = document.getElementById("publish-status-text");
var publishInventoryButton = document.getElementById("publish-inventory");
var publishPollTimer = null;

function renderPublishStatus(data) {
  if (!publishStatusText) return;
  if (data.running) {
    if (data.phase === "scanning") {
      var platformPart = data.platform ?
        " " + data.platform + " (platform " + data.platform_index + " of " + data.platform_total + ")" : "";
      publishStatusText.textContent = "Scanning" + platformPart + " — " + data.items_scanned + " item(s) scanned so far…";
    } else {
      publishStatusText.textContent = "Publishing to the Host…";
    }
    publishStatusText.className = "";
  } else if (data.phase === "error") {
    publishStatusText.textContent = data.error || "Publish failed";
    publishStatusText.className = "status-bad";
  } else if (data.phase === "done" && data.result) {
    if (data.result.published) {
      publishStatusText.textContent = "Published revision " + data.result.revision + ": " +
        data.result.item_count + " item(s), " + data.result.distinct_files + " distinct file(s) swarm-wide";
      publishStatusText.className = "status-ok";
    } else {
      var reasons = data.result.skip_reasons ? Object.keys(data.result.skip_reasons).map(function (k) {
        return k + " (" + data.result.skip_reasons[k] + ")";
      }).join(", ") : "";
      publishStatusText.textContent = "Nothing published — " + (reasons || "no eligible holdings found");
      publishStatusText.className = "status-bad";
    }
  }
}

function pollPublishStatus() {
  fetch("/api/swarm/publish-status")
    .then(function (resp) { return resp.json(); })
    .then(function (data) {
      renderPublishStatus(data);
      publishPollTimer = data.running ? setTimeout(pollPublishStatus, 1500) : null;
    })
    .catch(function () {
      publishPollTimer = setTimeout(pollPublishStatus, 3000);
    });
}

if (publishStatusDiv && publishStatusDiv.dataset.running === "true") {
  pollPublishStatus();
}

if (publishInventoryButton) {
  publishInventoryButton.addEventListener("click", async function () {
    try {
      var resp = await fetch("/api/swarm/publish-inventory", { method: "POST" });
      var data = await resp.json();
      renderPublishStatus(data);
      if (data.running && !publishPollTimer) {
        publishPollTimer = setTimeout(pollPublishStatus, 1500);
      }
    } catch (e) {
      if (publishStatusText) {
        publishStatusText.textContent = "Request failed: " + e;
        publishStatusText.className = "status-bad";
      }
    }
  });
}
