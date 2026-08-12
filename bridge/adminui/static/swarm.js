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
