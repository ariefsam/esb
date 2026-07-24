/* esb ui — local helper script.
 *
 * The UI uses Alpine-style reactive patterns (per-form state, live
 * preview, polling) but ships without Alpine.js so the binary stays
 * self-contained. The helpers below cover the four interactions the
 * plan calls out: live argv preview, disable-on-submit, polling, and
 * collapsible output sections.
 */
(function () {
  function escAttr(v) {
    return String(v).replace(/[&"<>]/g, function (c) {
      return ({ "&": "&amp;", '"': "&quot;", "<": "&lt;", ">": "&gt;" })[c];
    });
  }

  function parseFields(form) {
    var fields = [];
    var nodes = form.querySelectorAll("[data-field-name]");
    for (var i = 0; i < nodes.length; i++) {
      var n = nodes[i];
      fields.push({
        name: n.getAttribute("data-field-name"),
        type: n.getAttribute("data-field-type") || "text",
      });
    }
    return fields;
  }

  function previewFor(form, fields) {
    var parts = ["esb"];
    // Command id is in the first hidden input "command" — we read the
    // surrounding form's title which we stashed in data-cmd.
    var cmd = form.getAttribute("data-cmd") || "";
    parts = parts.concat(cmdToArgv(cmd));
    fields.forEach(function (f) {
      var node = form.querySelector('[name="' + escAttr(f.name) + '"]');
      if (!node) return;
      var raw = node.value || "";
      if (f.type === "list") {
        raw.split(/\r?\n/).forEach(function (line) {
          var t = line.trim();
          if (t) parts.push(t);
        });
      } else {
        var t = raw.trim();
        if (t) parts.push(t);
      }
    });
    return parts.join(" ");
  }

  function cmdToArgv(cmd) {
    switch (cmd) {
      case "add-aggregate": return ["add", "aggregate"];
      case "add-event": return ["add", "event"];
      case "add-projection": return ["add", "projection"];
      case "add-handler": return ["add", "handler"];
      case "add-query": return ["add", "query"];
      case "show": return ["show"];
      default: return [];
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    document.querySelectorAll("form[data-cmd]").forEach(function (form) {
      var fields = parseFields(form);
      var out = form.querySelector("[data-preview]");
      if (out) {
        var render = function () {
          out.textContent = previewFor(form, fields);
        };
        render();
        form.addEventListener("input", render);
      }
      form.addEventListener("submit", function () {
        var btn = form.querySelector("button[type=submit]");
        if (btn) btn.disabled = true;
      });
    });

    var run = document.querySelector("[data-run-status]");
    if (run && run.getAttribute("data-run-status") === "running") {
      setTimeout(function () { window.location.reload(); }, 1500);
    }
  });
})();