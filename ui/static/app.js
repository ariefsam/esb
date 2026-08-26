/* esb ui — local helper script.
 *
 * The UI uses Alpine-style reactive patterns (per-form state, live
 * preview, polling) but ships without Alpine.js so the binary stays
 * self-contained. The helpers below cover the four interactions the
 * plan calls out: live argv preview, disable-on-submit, polling, and
 * collapsible output sections.
 */
(function () {
  var supportedTypes = ["string", "int", "int64", "float64", "bool", "time.Time", "uuid.UUID"];

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

  function initFieldBuilders() {
    document.querySelectorAll(".field-builder-wrapper").forEach(function (wrapper) {
      var fieldName = wrapper.getAttribute("data-field-name");
      var textarea = document.getElementById("field-" + fieldName);
      var inputs = wrapper.querySelector(".field-builder-inputs");
      var addButton = wrapper.querySelector(".btn-add-field");
      var suggestions = (wrapper.getAttribute("data-suggestions") || "").split(",").filter(Boolean);
      if (!textarea || !inputs || !addButton) return;

      var sync = function () {
        var lines = [];
        inputs.querySelectorAll(".field-builder-row").forEach(function (row) {
          var name = row.querySelector(".field-name-input").value.trim();
          var type = row.querySelector(".field-type-select").value;
          if (name && type) lines.push(name + ":" + type);
        });
        textarea.value = lines.join("\n");
        textarea.dispatchEvent(new Event("input", { bubbles: true }));
      };

      var addRow = function (name, type) {
        var row = document.createElement("div");
        row.className = "field-builder-row";

        var nameInput = document.createElement("input");
        nameInput.type = "text";
        nameInput.className = "field-name-input";
        nameInput.placeholder = "fieldName";
        nameInput.value = name || "";
        if (suggestions.length) {
          var listID = "suggestions-" + fieldName;
          var datalist = document.getElementById(listID);
          if (!datalist) {
            datalist = document.createElement("datalist");
            datalist.id = listID;
            suggestions.forEach(function (suggestion) {
              var option = document.createElement("option");
              option.value = suggestion;
              datalist.appendChild(option);
            });
            wrapper.appendChild(datalist);
          }
          nameInput.setAttribute("list", listID);
        }

        var typeSelect = document.createElement("select");
        typeSelect.className = "field-type-select";
        supportedTypes.forEach(function (supportedType) {
          var option = document.createElement("option");
          option.value = supportedType;
          option.textContent = supportedType;
          option.selected = supportedType === (type || "string");
          typeSelect.appendChild(option);
        });

        var removeButton = document.createElement("button");
        removeButton.type = "button";
        removeButton.className = "btn-remove-field";
        removeButton.title = "Remove field";
        removeButton.textContent = "×";

        nameInput.addEventListener("input", sync);
        typeSelect.addEventListener("change", sync);
        removeButton.addEventListener("click", function () {
          row.remove();
          sync();
        });
        row.appendChild(nameInput);
        row.appendChild(typeSelect);
        row.appendChild(removeButton);
        inputs.appendChild(row);
      };

      var lines = textarea.value.split(/\r?\n/).filter(function (line) {
        return line.trim();
      });
      inputs.innerHTML = "";
      if (lines.length) {
        lines.forEach(function (line) {
          var pair = line.split(":");
          if (pair[0] && pair[1]) addRow(pair[0].trim(), pair.slice(1).join(":").trim());
        });
      } else {
        addRow();
      }
      addButton.addEventListener("click", function () {
        addRow();
      });
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    initFieldBuilders();
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