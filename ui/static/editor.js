/* esb ui — aggregate editor (/editor/{aggregate}).
 *
 * Left: the aggregate's flow diagram (server-rendered SVG, refreshed after
 * every save). Right: Monaco, one model per file, so switching files keeps
 * unsaved edits. Clicking a node opens its file at its line; moving the
 * cursor lights the node whose code it is in.
 *
 * The server is the authority: a save is gofmt'ed there and refused (409)
 * when the file changed on disk since it was loaded; Check runs go vet on
 * what is on disk; History and Commit go through git.
 */
(function () {
  var app = document.getElementById("ed");
  if (!app) return;

  var AGG = app.getAttribute("data-aggregate");
  var API = "/editor/" + encodeURIComponent(AGG);
  var base = app.getAttribute("data-monaco-base");
  var WORKER = base + "/assets/editor.worker-_vAIFJDs.js";
  var GO_MODULE = "vs/go-D_hbi-Jt";
  var VIEW_KEY = "esb.editor.view";

  var body = document.getElementById("ed-body");
  var diagramEl = document.getElementById("ed-diagram");
  var monacoEl = document.getElementById("ed-monaco");
  var fileSel = document.getElementById("ed-file");
  var saveBtn = document.getElementById("ed-save");
  var checkBtn = document.getElementById("ed-check");
  var statusEl = document.getElementById("ed-status");
  var problemsEl = document.getElementById("ed-problems");

  var monaco = null;
  var editor = null;
  var files = {}; // path -> { model, hash, saved }
  var current = null;
  var vetProblems = []; // from the last Check, for every file
  var lineDecorations = [];
  var selectedNode = null;

  // ---- helpers ---------------------------------------------------------------

  function status(msg, kind, actions) {
    statusEl.textContent = msg;
    statusEl.className = "ed-status" + (kind ? " is-" + kind : "");
    (actions || []).forEach(function (a) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "btn small";
      b.textContent = a.label;
      b.addEventListener("click", a.run);
      statusEl.appendChild(b);
    });
  }

  function api(path, opts) {
    return fetch(API + path, opts).then(function (r) {
      return r.json()
        .catch(function () { return { error: "HTTP " + r.status }; })
        .then(function (body) { return { ok: r.ok, status: r.status, body: body }; });
    });
  }

  function post(path, data) {
    return api(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data || {}),
    });
  }

  function isEditable(path) {
    return !!(fileSel && fileSel.querySelector('option[value="' + CSS.escape(path) + '"]'));
  }

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
    return loadScript(base + "/loader.js").then(function () {
      window.MonacoEnvironment = { getWorker: function () { return new Worker(WORKER); } };
      window.require.config({ paths: { vs: base } });
      return new Promise(function (resolve, reject) {
        window.require(["vs/editor", GO_MODULE], function (m, go) {
          m.languages.register({ id: "go" });
          m.languages.setLanguageConfiguration("go", go.conf);
          m.languages.setMonarchTokensProvider("go", go.language);
          resolve(m);
        }, reject);
      });
    });
  }

  // ---- dirty state -------------------------------------------------------------

  function isDirty(f) {
    return f.model.getAlternativeVersionId() !== f.saved;
  }

  function anyDirty() {
    return Object.keys(files).some(function (p) { return isDirty(files[p]); });
  }

  function refreshDirty() {
    if (fileSel) {
      Array.prototype.forEach.call(fileSel.options, function (o) {
        var f = files[o.value];
        o.textContent = (f && isDirty(f) ? "● " : "") + o.value;
      });
    }
    saveBtn.disabled = !(current && isDirty(files[current]));
    document.title = (anyDirty() ? "● " : "") + AGG + " — esb ui";
  }

  // ---- files -------------------------------------------------------------------

  function open(path, line) {
    if (!path || !editor) return Promise.resolve();
    var ready = files[path]
      ? Promise.resolve(files[path])
      : api("/file?path=" + encodeURIComponent(path)).then(function (res) {
          if (!res.ok) throw new Error(res.body.error || "HTTP " + res.status);
          var model = monaco.editor.createModel(res.body.code, "go", monaco.Uri.parse("inmemory://esb/" + path));
          var f = { model: model, hash: res.body.hash, saved: model.getAlternativeVersionId() };
          files[path] = f;
          model.onDidChangeContent(refreshDirty);
          applyVetMarkers(path);
          return f;
        });
    return ready.then(function (f) {
      if (current !== path) {
        editor.setModel(f.model);
        current = path;
        if (fileSel) fileSel.value = path;
        history.replaceState(null, "", "?file=" + encodeURIComponent(path));
      }
      if (line) {
        editor.setPosition({ lineNumber: line, column: 1 });
        editor.revealLineInCenter(line);
        lineDecorations = editor.deltaDecorations(lineDecorations, [{
          range: new monaco.Range(line, 1, line, 1),
          options: { isWholeLine: true, className: "ed-line" },
        }]);
      }
      refreshDirty();
      markCursorNode();
    }).catch(function (err) {
      status("Gagal membuka " + path + ": " + err.message, "error");
    });
  }

  function replaceAll(f, text) {
    f.model.pushEditOperations([], [{ range: f.model.getFullModelRange(), text: text }], function () { return null; });
  }

  function save() {
    var path = current;
    var f = path && files[path];
    if (!f || !isDirty(f)) return;
    var code = f.model.getValue();
    var version = f.model.getAlternativeVersionId();
    saveBtn.disabled = true;
    status("Menyimpan " + path + "…");
    post("/save", { path: path, code: code, hash: f.hash }).then(function (res) {
      monaco.editor.setModelMarkers(f.model, "esb-parse", []);
      if (res.ok) {
        f.hash = res.body.hash;
        var untouched = f.model.getAlternativeVersionId() === version;
        // gofmt may have changed the file; take its result unless the user
        // kept typing while the save was in flight.
        if (untouched && res.body.code !== code) replaceAll(f, res.body.code);
        f.saved = untouched ? f.model.getAlternativeVersionId() : version;
        refreshDirty();
        status("Tersimpan: " + path + " (" + new Date().toLocaleTimeString() + ")", "ok");
        refreshDiagram();
        return;
      }
      refreshDirty();
      if (res.status === 422) {
        var problems = res.body.problems || [];
        monaco.editor.setModelMarkers(f.model, "esb-parse", problems.map(marker));
        showProblems(problems, "");
        status(res.body.error, "error");
      } else if (res.body.stale) {
        status("Gagal: " + path + " berubah di disk sejak dibuka.", "error", [
          { label: "Muat versi disk (buang perubahan saya)", run: function () {
            replaceAll(f, res.body.code);
            f.hash = res.body.hash;
            f.saved = f.model.getAlternativeVersionId();
            refreshDirty();
            status("Versi disk dimuat.", "ok");
          } },
          { label: "Timpa dengan versi saya", run: function () {
            f.hash = res.body.hash;
            save();
          } },
        ]);
      } else {
        status(res.body.error || "Gagal menyimpan (HTTP " + res.status + ")", "error");
      }
    }).catch(function (err) {
      refreshDirty();
      status("Gagal menyimpan: " + err.message, "error");
    });
  }

  // ---- problems (parse errors on save, go vet on Check) --------------------------

  function marker(p) {
    return {
      severity: monaco.MarkerSeverity.Error,
      message: p.message,
      startLineNumber: p.line,
      startColumn: p.column || 1,
      endLineNumber: p.line,
      endColumn: (p.column || 1) + 1,
    };
  }

  function applyVetMarkers(path) {
    var f = files[path];
    if (!f) return;
    monaco.editor.setModelMarkers(f.model, "esb-vet", vetProblems.filter(function (p) {
      return p.file === path;
    }).map(marker));
  }

  function showProblems(problems, output) {
    problemsEl.textContent = "";
    problems.forEach(function (p) {
      var b = document.createElement("button");
      b.type = "button";
      b.textContent = p.file + ":" + p.line + ":" + p.column + "  " + p.message;
      b.addEventListener("click", function () {
        if (isEditable(p.file)) open(p.file, p.line);
        else status(p.file + " bukan bagian dari aggregate ini — buka dari aggregate pemiliknya.", "error");
      });
      problemsEl.appendChild(b);
    });
    if (output) {
      var pre = document.createElement("pre");
      pre.textContent = output;
      problemsEl.appendChild(pre);
    }
    problemsEl.hidden = problems.length === 0 && !output;
  }

  function check() {
    checkBtn.disabled = true;
    var note = anyDirty() ? " Ada perubahan belum disimpan: Check memakai file di disk." : "";
    status("Menjalankan go vet ./…" + note);
    post("/check").then(function (res) {
      checkBtn.disabled = false;
      if (!res.ok) {
        status(res.body.error || "Check gagal (HTTP " + res.status + ")", "error");
        return;
      }
      vetProblems = res.body.problems || [];
      Object.keys(files).forEach(applyVetMarkers);
      showProblems(vetProblems, res.body.ok ? "" : res.body.output);
      if (res.body.ok) status("go vet: tidak ada masalah." + note, "ok");
      else status("go vet: " + vetProblems.length + " masalah." + note, "error");
    }).catch(function (err) {
      checkBtn.disabled = false;
      status("Check gagal: " + err.message, "error");
    });
  }

  // ---- diagram -------------------------------------------------------------------

  function svg() {
    return diagramEl.querySelector("svg");
  }

  function selectNode(node) {
    var s = svg();
    if (!s) return;
    s.querySelectorAll(".is-on, .is-selected").forEach(function (el) {
      el.classList.remove("is-on", "is-selected");
    });
    selectedNode = node ? node.getAttribute("data-id") : null;
    s.classList.toggle("is-focused", !!node);
    if (!node) return;
    node.classList.add("is-selected");
    s.querySelectorAll(".flow-edge").forEach(function (e) {
      var from = e.getAttribute("data-from"), to = e.getAttribute("data-to");
      if (from !== selectedNode && to !== selectedNode) return;
      e.classList.add("is-on");
      var other = s.querySelector('.flow-node[data-id="' + CSS.escape(from === selectedNode ? to : from) + '"]');
      if (other) other.classList.add("is-on");
    });
  }

  function activate(node) {
    selectNode(node);
    var file = node.getAttribute("data-file");
    if (!file) {
      status("Node ini tidak punya kode yang bisa dibuka.");
    } else if (!isEditable(file)) {
      status(file + " bukan bagian dari aggregate ini.");
    } else {
      open(file, parseInt(node.getAttribute("data-line"), 10) || 1);
    }
  }

  diagramEl.addEventListener("click", function (e) {
    var node = e.target.closest(".flow-node");
    if (node) activate(node);
    else selectNode(null);
  });
  diagramEl.addEventListener("keydown", function (e) {
    var node = e.target.closest && e.target.closest(".flow-node");
    if (node && (e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      activate(node);
    }
  });

  // The node whose code holds the cursor: of the current file's nodes, the
  // one declared last at or above the cursor line. Nodes only know where
  // their declaration starts, so this is the closest match the diagram has.
  function markCursorNode() {
    var s = svg();
    if (!s || !editor || !current) return;
    var line = editor.getPosition() ? editor.getPosition().lineNumber : 1;
    var best = null, bestLine = 0;
    s.querySelectorAll(".flow-node[data-file]").forEach(function (n) {
      n.classList.remove("is-cursor");
      if (n.getAttribute("data-file") !== current) return;
      var l = parseInt(n.getAttribute("data-line"), 10) || 0;
      if (l <= line && l >= bestLine) {
        best = n;
        bestLine = l;
      }
    });
    if (best) best.classList.add("is-cursor");
  }

  function refreshDiagram() {
    fetch(API + "/diagram").then(function (r) {
      if (!r.ok) throw new Error("HTTP " + r.status);
      return r.text();
    }).then(function (html) {
      diagramEl.classList.remove("is-stale");
      diagramEl.innerHTML = html; // same-origin, rendered by html/template
      var keep = selectedNode && svg() && svg().querySelector('.flow-node[data-id="' + CSS.escape(selectedNode) + '"]');
      selectNode(keep || null);
      markCursorNode();
    }).catch(function (err) {
      diagramEl.classList.add("is-stale");
      status("Diagram tidak bisa diperbarui: " + err.message, "error");
    });
  }

  // ---- views -----------------------------------------------------------------------

  function setView(v) {
    if (["split", "diagram", "code"].indexOf(v) < 0) v = "split";
    body.className = "ed-body view-" + v;
    document.querySelectorAll(".ed-views button").forEach(function (b) {
      b.setAttribute("aria-pressed", b.getAttribute("data-view") === v ? "true" : "false");
    });
    try { localStorage.setItem(VIEW_KEY, v); } catch (e) { /* private mode */ }
    if (editor) editor.layout();
  }
  document.querySelectorAll(".ed-views button").forEach(function (b) {
    b.addEventListener("click", function () { setView(b.getAttribute("data-view")); });
  });
  try { setView(localStorage.getItem(VIEW_KEY) || "split"); } catch (e) { setView("split"); }

  // ---- history -------------------------------------------------------------------

  var historyDlg = document.getElementById("ed-history");
  var historyList = document.getElementById("ed-history-list");
  var historyStatus = document.getElementById("ed-history-status");
  var restoreBtn = document.getElementById("ed-history-restore");
  var diffEditor = null;
  var picked = null; // { short, code }

  function showDiff(code) {
    if (!diffEditor) {
      diffEditor = monaco.editor.createDiffEditor(document.getElementById("ed-diff"), {
        readOnly: true,
        originalEditable: false,
        automaticLayout: true,
        renderSideBySide: true,
        minimap: { enabled: false },
      });
    }
    var old = diffEditor.getModel();
    diffEditor.setModel({
      original: monaco.editor.createModel(code, "go"),
      modified: monaco.editor.createModel(files[current].model.getValue(), "go"),
    });
    if (old) {
      old.original.dispose();
      old.modified.dispose();
    }
  }

  function pick(entry, button) {
    historyList.querySelectorAll("button").forEach(function (b) { b.classList.remove("active"); });
    button.classList.add("active");
    historyStatus.textContent = "Memuat " + entry.short + "…";
    api("/version?path=" + encodeURIComponent(current) + "&commit=" + encodeURIComponent(entry.commit)).then(function (res) {
      if (!res.ok) throw new Error(res.body.error || "HTTP " + res.status);
      picked = { short: entry.short, code: res.body.code };
      restoreBtn.disabled = false;
      historyStatus.textContent = "Kiri: " + entry.short + " — kanan: isi editor sekarang.";
      showDiff(res.body.code);
    }).catch(function (err) {
      historyStatus.textContent = "Gagal memuat versi: " + err.message;
    });
  }

  document.getElementById("ed-history-open").addEventListener("click", function () {
    if (!current || !monaco) return;
    picked = null;
    restoreBtn.disabled = true;
    historyList.textContent = "";
    document.getElementById("ed-history-title").textContent = "History — " + current;
    historyStatus.textContent = "Memuat…";
    historyDlg.showModal();
    api("/history?path=" + encodeURIComponent(current)).then(function (res) {
      if (!res.ok) throw new Error(res.body.error || "HTTP " + res.status);
      if (!res.body.available) {
        historyStatus.textContent = res.body.reason;
        return;
      }
      var entries = res.body.entries || [];
      historyStatus.textContent = entries.length ? "Pilih commit untuk melihat bedanya." : "Belum ada commit untuk file ini.";
      entries.forEach(function (e) {
        var b = document.createElement("button");
        b.type = "button";
        var subject = document.createElement("span");
        subject.textContent = e.subject;
        var meta = document.createElement("small");
        meta.textContent = e.short + " · " + e.author + " · " + new Date(e.date).toLocaleString();
        b.appendChild(subject);
        b.appendChild(meta);
        b.addEventListener("click", function () { pick(e, b); });
        historyList.appendChild(b);
      });
    }).catch(function (err) {
      historyStatus.textContent = "Gagal memuat history: " + err.message;
    });
  });
  document.getElementById("ed-history-close").addEventListener("click", function () { historyDlg.close(); });
  restoreBtn.addEventListener("click", function () {
    if (!picked || !current) return;
    replaceAll(files[current], picked.code);
    historyDlg.close();
    status("Versi " + picked.short + " dipulihkan ke editor — belum disimpan.", "ok");
  });

  // ---- commit ----------------------------------------------------------------------

  var commitDlg = document.getElementById("ed-commit");
  var commitMsg = document.getElementById("ed-commit-message");
  var commitStatus = document.getElementById("ed-commit-status");
  var commitSubmit = document.getElementById("ed-commit-submit");

  document.getElementById("ed-commit-open").addEventListener("click", function () {
    var dirty = anyDirty();
    commitSubmit.disabled = dirty;
    commitStatus.textContent = dirty ? "Ada perubahan yang belum disimpan. Simpan dulu, lalu commit." : "";
    commitDlg.showModal();
    commitMsg.focus();
  });
  document.getElementById("ed-commit-cancel").addEventListener("click", function () { commitDlg.close(); });
  document.getElementById("ed-commit-form").addEventListener("submit", function (e) {
    e.preventDefault();
    commitSubmit.disabled = true;
    commitStatus.textContent = "Commit…";
    post("/commit", { message: commitMsg.value }).then(function (res) {
      commitSubmit.disabled = false;
      if (!res.ok) {
        commitStatus.textContent = res.body.error || "Commit gagal (HTTP " + res.status + ")";
        return;
      }
      if (!res.body.committed) {
        commitStatus.textContent = "Tidak ada perubahan di file aggregate ini untuk di-commit.";
        return;
      }
      commitMsg.value = "";
      commitDlg.close();
      status("Commit " + res.body.commit + " dibuat.", "ok");
    }).catch(function (err) {
      commitSubmit.disabled = false;
      commitStatus.textContent = "Commit gagal: " + err.message;
    });
  });

  // ---- start -------------------------------------------------------------------------

  checkBtn.addEventListener("click", check);
  saveBtn.addEventListener("click", save);
  if (fileSel) fileSel.addEventListener("change", function () { open(fileSel.value); });

  window.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && (e.key === "s" || e.key === "S")) {
      e.preventDefault();
      save();
    }
  });
  window.addEventListener("beforeunload", function (e) {
    if (anyDirty()) {
      e.preventDefault();
      e.returnValue = "";
    }
  });

  if (!monacoEl) {
    status("");
    return;
  }
  loadMonaco().then(function (m) {
    monaco = m;
    editor = monaco.editor.create(monacoEl, {
      model: null,
      automaticLayout: true,
      minimap: { enabled: false },
      scrollBeyondLastLine: false,
      fontSize: 13,
      tabSize: 4,
      insertSpaces: false,
      theme: "vs",
    });
    editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, save);
    editor.onDidChangeCursorPosition(markCursorNode);
    var line = parseInt(app.getAttribute("data-line"), 10) || 1;
    return open(app.getAttribute("data-initial"), line > 1 ? line : 0);
  }).then(function () {
    status("Siap. Ctrl/⌘+S untuk simpan, klik node untuk membuka kodenya.");
  }).catch(function (err) {
    status("Gagal memuat editor: " + err.message, "error");
  });
})();
