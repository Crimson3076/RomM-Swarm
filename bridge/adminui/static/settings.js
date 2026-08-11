document.getElementById("test-connection").addEventListener("click", async function () {
  var result = document.getElementById("test-connection-result");
  var url = document.getElementById("romm_url").value;
  var token = document.getElementById("romm_token").value;
  result.textContent = "Testing...";
  try {
    var resp = await fetch("/api/connection/test", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: "romm_url=" + encodeURIComponent(url) + "&romm_token=" + encodeURIComponent(token),
    });
    var data = await resp.json();
    if (resp.ok) {
      result.textContent = "Connected" + (data.server_version ? " (RomM " + data.server_version + ")" : "");
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
