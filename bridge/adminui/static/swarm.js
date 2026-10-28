(() => {
  const ui = window.SwarmUI;
  const testButton = document.getElementById("test-swarm");
  testButton?.addEventListener("click", async () => {
    const result = document.getElementById("test-swarm-result");
    testButton.disabled = true;
    result.className = "";
    result.textContent = "Testing the Host connection...";
    try {
      const data = await ui.request("/api/swarm/test", {method: "POST"});
      result.textContent = "Connected. Credential generation " + data.generation + ".";
      result.className = "status-ok";
    } catch (error) {
      result.textContent = error.message;
      result.className = "status-bad";
    } finally { testButton.disabled = false; }
  });

  const button = document.getElementById("publish-inventory");
  const output = document.getElementById("publish-status-text");
  if (!button || !output) return;
  let timer, polling = false, posting = false, running = false, sequence = 0, unauthorized = false;
  function render(data) {
    running = Boolean(data.running);
    button.disabled = running || posting;
    button.textContent = running ? "Publishing inventory..." : "Publish Inventory";
    output.className = "";
    if (running) {
      output.textContent = data.phase === "scanning"
        ? "Scanning " + (data.platform || "library") + " (" + data.platform_index + "/" + data.platform_total + " platforms), " + data.items_scanned + " items checked."
        : "Sending the verified inventory to the Host...";
    } else if (data.phase === "error") {
      output.textContent = data.error || "Inventory publishing failed.";
      output.className = "status-bad";
    } else if (data.phase === "done" && data.result) {
      if (data.result.published) {
        output.textContent = "Published revision " + data.result.revision + ": " + data.result.item_count +
          " holdings, " + data.result.skipped_count + " skipped. The Swarm reports " + data.result.distinct_files + " distinct files.";
        output.className = "status-ok";
      } else {
        const reasons = Object.entries(data.result.skip_reasons || {}).map(([reason, count]) => reason + " (" + count + ")").join("; ");
        output.textContent = "Nothing eligible to publish. " + (reasons || "Ask the Host administrator to check the reference catalogues.");
      }
    } else {
      output.textContent = "Ready to publish. Eligible holdings are verified against the Host's reference catalogues.";
    }
  }
  function schedule() {
    clearTimeout(timer);
    if (!document.hidden && !unauthorized) timer = setTimeout(poll, running ? 1500 : 8000);
  }
  async function poll() {
    if (polling || posting || document.hidden || unauthorized) return;
    polling = true;
    const version = sequence;
    try {
      const data = await ui.request("/api/swarm/publish-status");
      if (version === sequence) render(data);
    } catch (error) {
      if (version === sequence) {
        output.textContent = error.message;
        output.className = "status-bad";
        unauthorized = error.status === 401;
      }
    } finally {
      polling = false;
      if (!posting) schedule();
    }
  }
  button.addEventListener("click", async () => {
    if (posting || running) return;
    posting = true;
    sequence++;
    clearTimeout(timer);
    button.disabled = true;
    output.textContent = "Starting inventory publish...";
    try { render(await ui.request("/api/swarm/publish-inventory", {method: "POST"})); }
    catch (error) {
      output.textContent = error.message;
      output.className = "status-bad";
      unauthorized = error.status === 401;
    } finally {
      posting = false;
      button.disabled = running;
      schedule();
    }
  });
  document.addEventListener("visibilitychange", () => {
    clearTimeout(timer);
    if (!document.hidden) poll();
  });
  poll();
})();
