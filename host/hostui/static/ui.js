/* Shared browser helpers. All API responses and user text stay out of innerHTML. */
window.SwarmUI = {
  async request(url, options) {
    const response = await fetch(url, {
      ...options,
      credentials: "same-origin",
      cache: "no-store",
      headers: {Accept: "application/json", ...options?.headers}
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(response.status === 401
        ? "Your session expired. Log in again to continue."
        : body.error || "Request failed (" + response.status + "). Try again.");
      error.status = response.status;
      throw error;
    }
    return body;
  },
  time(value) {
    if (!value) return "";
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
  },
  element(tag, text, className) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  }
};
document.querySelectorAll("time[datetime]").forEach(node => {
  node.textContent = window.SwarmUI.time(node.dateTime);
});
