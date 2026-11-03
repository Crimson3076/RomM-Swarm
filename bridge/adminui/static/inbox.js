(() => {
  const ui = window.SwarmUI;
  document.querySelectorAll(".import-form").forEach(form => {
    let busy = false;
    form.addEventListener("submit", event => {
      event.preventDefault();
      if (busy) return;
      const button = form.querySelector('button[type="submit"]');
      const result = form.querySelector(".import-result");
      const progress = form.querySelector(".import-progress");
      const file = form.querySelector('input[type="file"]')?.files[0];
      if (file && file.size > 1024 * 1024 * 1024) {
        result.className = "import-result status-bad";
        result.textContent = "This file exceeds the 1 GiB browser upload limit.";
        return;
      }
      const data = new FormData(form);
      busy = true;
      button.disabled = true;
      result.className = "import-result";
      result.textContent = file ? "Uploading to the Bridge..." : "Validating the inbox file...";
      if (progress) { progress.hidden = false; progress.value = 0; }
      const xhr = new XMLHttpRequest();
      xhr.open("POST", form.action);
      xhr.setRequestHeader("Accept", "application/json");
      xhr.responseType = "json";
      xhr.timeout = 30 * 60 * 1000;
      xhr.upload.addEventListener("progress", e => {
        if (e.lengthComputable && progress) {
          progress.value = Math.round(e.loaded * 100 / e.total);
          result.textContent = progress.value === 100
            ? "Upload received. Validating and starting the import..."
            : "Uploading to the Bridge: " + progress.value + "%";
        }
      });
      function finish() {
        busy = false;
        button.disabled = false;
        if (progress) progress.hidden = true;
      }
      function fail(message) {
        result.className = "import-result status-bad";
        result.textContent = message;
      }
      xhr.addEventListener("load", () => {
        finish();
        if (xhr.status === 202 && xhr.response?.id) {
          result.className = "import-result status-ok";
          result.replaceChildren(ui.element("span", "Import started. "));
          const link = ui.element("a", "View progress");
          link.href = "/activity?" + new URLSearchParams({id: xhr.response.id});
          result.append(link);
          if (file) form.querySelector('input[type="file"]').value = "";
        } else if (xhr.status === 401) {
          fail("Your session expired. Log in again before importing.");
        } else {
          fail(xhr.response?.error || "The import could not be started. Check Activity before retrying.");
        }
      });
      ["error", "timeout", "abort"].forEach(name => xhr.addEventListener(name, () => {
        finish();
        fail("The request was interrupted. Check Activity before retrying, because the import may have started.");
      }));
      xhr.send(data);
    });
  });
})();
