(() => {
  const button = document.getElementById("test-connection");
  if (!button) return;
  button.addEventListener("click", async () => {
    const result = document.getElementById("test-connection-result");
    result.textContent = "Testing connection...";
    result.className = "";
    button.disabled = true;
    try {
      const data = await window.SwarmUI.request("/api/connection/test", {
        method: "POST",
        body: new URLSearchParams({
          romm_url: document.getElementById("romm_url").value,
          romm_token: document.getElementById("romm_token").value
        })
      });
      result.textContent = "Connected" + (data.server_version ? " (RomM " + data.server_version + ")" : "") + ".";
      result.className = "status-ok";
    } catch (error) {
      result.textContent = error.message;
      result.className = "status-bad";
    } finally { button.disabled = false; }
  });
})();
