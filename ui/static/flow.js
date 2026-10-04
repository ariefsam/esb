/* esb ui — Flow page interactions.
 *
 * The graph itself is server-rendered SVG (see ui/flow.go). This script adds,
 * without any library (the binary runs offline):
 *
 *   - path highlighting: selecting a node (click, keyboard or search) lights
 *     up everything upstream to the handlers and downstream to the queries,
 *     and dims the rest. The selection is kept in ?focus= so it can be shared.
 *   - filters that apply on change: the form is fetched as HTML and only the
 *     graph is swapped, so there is no full reload and no "Apply" button.
 *   - edge-type toggles, zoom/fit, and column titles that stay visible while
 *     scrolling the canvas.
 *   - a read-only Monaco viewer for the selected node's declaration. Monaco is
 *     vendored under /static/monaco and only loaded when first asked for.
 */
(function () {
  var form = document.getElementById("flow-filter");
  if (!form) return;

  var search = document.getElementById("flow-search");
  var detail = document.getElementById("flow-detail");
  var detailTitle = document.getElementById("flow-detail-title");
  var detailSub = document.getElementById("flow-detail-sub");
  var detailPath = document.getElementById("flow-detail-path");
  var detailCode = document.getElementById("flow-detail-code");
  var filterStatus = document.getElementById("flow-filter-status");

  var hiddenEdges = {}; // edge kind → true when toggled off
  var zoom = null;      // null = fill the canvas (default CSS sizing)
  var zoomed = false;   // the user picked a zoom; stop auto-fitting
  var selected = null;  // selected node ID

  // Graph index, rebuilt after every swap.
  var canvas, svg, head, nodes, outs, ins, edgesByNode;

  function index() {
    canvas = document.getElementById("flow-canvas");
    svg = document.getElementById("flow-svg");
    head = document.getElementById("flow-head");
    nodes = {};
    outs = {};
    ins = {};
    edgesByNode = {};
    if (!svg) return;
    svg.querySelectorAll(".flow-node[data-id]").forEach(function (n) {
      nodes[n.getAttribute("data-id")] = n;
    });
    svg.querySelectorAll(".flow-edge[data-from]").forEach(function (e) {
      var from = e.getAttribute("data-from");
      var to = e.getAttribute("data-to");
      (outs[from] = outs[from] || []).push(to);
      (ins[to] = ins[to] || []).push(from);
      (edgesByNode[from] = edgesByNode[from] || []).push(e);
      (edgesByNode[to] = edgesByNode[to] || []).push(e);
    });
    canvas.addEventListener("scroll", pinHead);
    applyEdgeToggles();
    autoFit();
    applyZoom();
    pinHead();
  }

  // ---- path highlighting ------------------------------------------------

  function reach(start, next) {
    var seen = {};
    var stack = [start];
    while (stack.length) {
      var id = stack.pop();
      (next[id] || []).forEach(function (n) {
        if (!seen[n]) {
          seen[n] = true;
          stack.push(n);
        }
      });
    }
    return seen;
  }

  function count(set) {
    return Object.keys(set).length;
  }

  function select(id, opts) {
    opts = opts || {};
    var node = nodes[id];
    if (!node) {
      if (opts.fromLink) {
        filterStatus.textContent = "Node itu tidak tampil dengan filter saat ini.";
      }
      return;
    }
    clearMarks();
    selected = id;
    var up = reach(id, ins);
    var down = reach(id, outs);
    var on = {};
    on[id] = true;
    Object.keys(up).forEach(function (k) { on[k] = true; });
    Object.keys(down).forEach(function (k) { on[k] = true; });

    svg.classList.add("is-focused");
    Object.keys(on).forEach(function (k) {
      if (nodes[k]) nodes[k].classList.add("is-on");
    });
    node.classList.add("is-selected");
    svg.querySelectorAll(".flow-edge[data-from]").forEach(function (e) {
      var from = e.getAttribute("data-from");
      var to = e.getAttribute("data-to");
      // Only edges along the path: both ends lit and on the same side of
      // the selected node (upstream → upstream/selected, or downstream).
      var upEdge = (up[from] && (up[to] || to === id));
      var downEdge = ((down[from] || from === id) && down[to]);
      if (upEdge || downEdge) e.classList.add("is-on");
    });

    detail.hidden = false;
    detailTitle.textContent = node.getAttribute("data-label");
    var warn = node.classList.contains("flow-node-warn");
    detailSub.textContent = warn ? "⚠ jalur buntu" : "";
    detailPath.textContent = count(up) + " node di hulu · " + count(down) + " node di hilir";
    detailCode.hidden = !node.getAttribute("data-file");

    setParam("focus", id);
    if (opts.scroll) scrollToNode(node);
  }

  function clearMarks() {
    if (!svg) return;
    svg.classList.remove("is-focused");
    svg.querySelectorAll(".is-on, .is-selected").forEach(function (el) {
      el.classList.remove("is-on", "is-selected");
    });
  }

  function clearSelection() {
    clearMarks();
    selected = null;
    detail.hidden = true;
    setParam("focus", null);
  }

  function scrollToNode(node) {
    var r = node.getBoundingClientRect();
    var c = canvas.getBoundingClientRect();
    canvas.scrollBy({
      left: r.left - c.left - (c.width - r.width) / 2,
      top: r.top - c.top - (c.height - r.height) / 2,
      behavior: "smooth",
    });
    canvas.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }

  function setParam(key, value) {
    var url = new URL(window.location.href);
    if (value) url.searchParams.set(key, value);
    else url.searchParams.delete(key);
    history.replaceState(null, "", url.pathname + url.search + url.hash);
  }

  // ---- search -------------------------------------------------------------

  function findByText(text) {
    var t = text.trim().toLowerCase();
    if (!t) return null;
    var ids = Object.keys(nodes);
    var exact = ids.filter(function (id) {
      return nodes[id].getAttribute("data-label").toLowerCase() === t;
    });
    if (exact.length) return exact[0];
    var starts = ids.filter(function (id) {
      return nodes[id].getAttribute("data-label").toLowerCase().indexOf(t) === 0;
    });
    if (starts.length) return starts[0];
    var any = ids.filter(function (id) {
      return nodes[id].getAttribute("data-label").toLowerCase().indexOf(t) >= 0;
    });
    return any.length ? any[0] : null;
  }

  function runSearch() {
    var id = findByText(search.value);
    if (id) {
      filterStatus.textContent = "";
      select(id, { scroll: true });
    } else if (search.value.trim()) {
      filterStatus.textContent = "Tidak ada node yang cocok dengan “" + search.value.trim() + "”.";
    }
  }

  search.addEventListener("change", runSearch);
  search.addEventListener("keydown", function (e) {
    if (e.key === "Enter") {
      e.preventDefault();
      runSearch();
    } else if (e.key === "Escape") {
      search.value = "";
      clearSelection();
    }
  });
  document.addEventListener("keydown", function (e) {
    var tag = (e.target.tagName || "").toLowerCase();
    var typing = tag === "input" || tag === "textarea" || e.target.isContentEditable;
    if (e.key === "/" && !typing) {
      e.preventDefault();
      search.focus();
    } else if (e.key === "Escape" && !typing && selected) {
      clearSelection();
    }
  });

  // ---- node interaction ---------------------------------------------------

  function nodeOf(target) {
    return target.closest ? target.closest(".flow-node[data-id]") : null;
  }

  document.addEventListener("click", function (e) {
    var n = nodeOf(e.target);
    if (n) {
      select(n.getAttribute("data-id"));
      return;
    }
    var code = e.target.closest && e.target.closest("[data-code-file]");
    if (code) {
      e.preventDefault();
      showCode(code.getAttribute("data-code-file"), parseInt(code.getAttribute("data-code-line"), 10) || 1);
      return;
    }
    var link = e.target.closest && e.target.closest("[data-focus]");
    if (link) {
      e.preventDefault();
      select(link.getAttribute("data-focus"), { scroll: true, fromLink: true });
    }
  });
  document.addEventListener("dblclick", function (e) {
    var n = nodeOf(e.target);
    if (n && n.getAttribute("data-file")) openCode(n);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Enter" && e.key !== " ") return;
    var n = nodeOf(e.target);
    if (n) {
      e.preventDefault();
      select(n.getAttribute("data-id"));
    }
  });
  document.getElementById("flow-detail-clear").addEventListener("click", clearSelection);
  detailCode.addEventListener("click", function () {
    if (selected && nodes[selected]) openCode(nodes[selected]);
  });

  // ---- edge toggles ---------------------------------------------------------

  document.querySelectorAll("[data-edge-toggle]").forEach(function (box) {
    box.addEventListener("change", function () {
      hiddenEdges[box.getAttribute("data-edge-toggle")] = !box.checked;
      applyEdgeToggles();
    });
  });

  function applyEdgeToggles() {
    if (!svg) return;
    ["call", "write", "read", "rm", "rmwrite", "inferred"].forEach(function (kind) {
      svg.classList.toggle("hide-" + kind, !!hiddenEdges[kind]);
    });
  }

  // ---- zoom and sticky column titles ------------------------------------

  function baseWidth() {
    return svg ? parseFloat(svg.getAttribute("data-width")) : 0;
  }

  function applyZoom() {
    if (!svg) return;
    if (zoom === null) {
      svg.style.width = "";
      svg.style.minWidth = baseWidth() + "px";
    } else {
      svg.style.minWidth = "0";
      svg.style.width = Math.round(baseWidth() * zoom) + "px";
    }
    pinHead();
  }

  // A graph slightly wider than the screen is shrunk to fit, so the last
  // columns are not cut off; one that would get too small keeps its natural
  // size and scrolls instead.
  function autoFit() {
    if (zoomed || !svg) return;
    var fit = (canvas.clientWidth - 16) / baseWidth();
    zoom = fit < 1 && fit >= 0.7 ? fit : null;
  }

  function currentScale() {
    return svg.getBoundingClientRect().width / baseWidth();
  }

  document.querySelectorAll("[data-zoom]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      if (!svg) return;
      var z = zoom === null ? currentScale() : zoom;
      var action = btn.getAttribute("data-zoom");
      if (action === "in") z = Math.min(z * 1.25, 3);
      else if (action === "out") z = Math.max(z / 1.25, 0.2);
      else z = (canvas.clientWidth - 16) / baseWidth();
      zoom = z;
      zoomed = true;
      applyZoom();
    });
  });

  // The column titles live in a <g> at the top of the SVG; keep it at the top
  // of the visible area as the canvas scrolls.
  function pinHead() {
    if (!svg || !head) return;
    var scale = currentScale() || 1;
    head.setAttribute("transform", "translate(0," + (canvas.scrollTop / scale) + ")");
  }

  // ---- filters that apply on change --------------------------------------

  var pending = null;

  function filterURL() {
    var params = new URLSearchParams(new FormData(form));
    if (selected) params.set("focus", selected);
    var qs = params.toString();
    return "/flow" + (qs ? "?" + qs : "");
  }

  function applyFilters() {
    var url = filterURL();
    filterStatus.textContent = "Memuat…";
    if (pending) pending.abort();
    pending = new AbortController();
    fetch(url, { signal: pending.signal, headers: { Accept: "text/html" } })
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.text();
      })
      .then(function (html) {
        var doc = new DOMParser().parseFromString(html, "text/html");
        ["flow-dynamic", "flow-edge-list"].forEach(function (id) {
          var fresh = doc.getElementById(id);
          var old = document.getElementById(id);
          if (fresh && old) old.replaceWith(fresh);
        });
        history.replaceState(null, "", url);
        filterStatus.textContent = "";
        index();
        // Keep the selection across filters: it lights up again as soon as
        // the node is back in the graph.
        if (selected && nodes[selected]) {
          select(selected);
        } else if (selected) {
          detail.hidden = true;
          filterStatus.textContent = "Node terpilih tidak tampil dengan filter ini.";
        }
      })
      .catch(function (err) {
        if (err.name !== "AbortError") filterStatus.textContent = "Gagal memuat graph: " + err.message;
      });
  }

  form.addEventListener("change", function (e) {
    if (e.target.name) applyFilters();
  });
  form.addEventListener("submit", function (e) {
    e.preventDefault();
    applyFilters();
  });
  document.getElementById("flow-reset").addEventListener("click", function (e) {
    e.preventDefault();
    form.querySelectorAll("input[type=checkbox]").forEach(function (b) { b.checked = false; });
    applyFilters();
  });

  // ---- Monaco code viewer ------------------------------------------------

  var panel = document.getElementById("flow-code");
  var host = document.getElementById("flow-code-editor");
  var codeTitle = document.getElementById("flow-code-title");
  var codeStatus = document.getElementById("flow-code-status");
  var base = host.getAttribute("data-monaco-base");
  var WORKER = base + "/assets/editor.worker-_vAIFJDs.js";
  var GO_MODULE = "vs/go-D_hbi-Jt";

  var monacoReady = null; // Promise, created on first use
  var editor = null;
  var decorations = [];
  var current = "";

  function loadScript(src) {
    return new Promise(function (resolve, reject) {
      var s = document.createElement("script");
      s.src = src;
      s.onload = resolve;
      s.onerror = function () { reject(new Error("gagal memuat " + src)); };
      document.head.appendChild(s);
    });
  }

  function loadMonaco() {
    if (monacoReady) return monacoReady;
    monacoReady = loadScript(base + "/loader.js").then(function () {
      window.MonacoEnvironment = { getWorker: function () { return new Worker(WORKER); } };
      window.require.config({ paths: { vs: base } });
      return new Promise(function (resolve, reject) {
        window.require(["vs/editor", GO_MODULE], function (monaco, go) {
          monaco.languages.register({ id: "go" });
          monaco.languages.setLanguageConfiguration("go", go.conf);
          monaco.languages.setMonarchTokensProvider("go", go.language);
          resolve(monaco);
        }, reject);
      });
    });
    return monacoReady;
  }

  function dark() {
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  }

  function showCode(file, line) {
    panel.hidden = false;
    panel.scrollIntoView({ block: "nearest", behavior: "smooth" });
    codeTitle.textContent = file + ":" + line;
    codeStatus.textContent = "Memuat…";
    var text = current === file
      ? Promise.resolve(null)
      : fetch("/flow/source?file=" + encodeURIComponent(file)).then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.text();
        });
    Promise.all([loadMonaco(), text]).then(function (res) {
      var monaco = res[0], src = res[1];
      if (!editor) {
        editor = monaco.editor.create(host, {
          language: "go",
          readOnly: true,
          automaticLayout: true,
          minimap: { enabled: false },
          scrollBeyondLastLine: false,
          theme: dark() ? "vs-dark" : "vs",
        });
      }
      if (src !== null) {
        editor.setValue(src);
        current = file;
      }
      decorations = editor.deltaDecorations(decorations, [{
        range: new monaco.Range(line, 1, line, 1),
        options: { isWholeLine: true, className: "flow-code-line" },
      }]);
      editor.revealLineInCenter(line);
      codeStatus.textContent = "Read-only. Dibaca dari source proyek.";
    }).catch(function (err) {
      codeStatus.textContent = "Gagal menampilkan kode: " + err.message;
    });
  }

  function openCode(node) {
    var file = node.getAttribute("data-file");
    var line = parseInt(node.getAttribute("data-line"), 10) || 1;
    if (file) showCode(file, line);
  }

  document.getElementById("flow-code-close").addEventListener("click", function () {
    panel.hidden = true;
  });

  // ---- start ----------------------------------------------------------------

  index();
  var focus = new URL(window.location.href).searchParams.get("focus");
  if (focus) select(focus, { scroll: true });
})();
