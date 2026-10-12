/* esb ui — sidebar: icon rail + collapsible explorer panel.
 *
 * Loaded (not deferred) from <head> so the stored open/closed state is on
 * <html> before the first paint. Closed means icon rail only, like the VS
 * Code activity bar; the panel opens from the toggle at the bottom of the
 * rail, from the Aggregates/Search icons, or with Ctrl/⌘+B. On a narrow
 * screen the panel overlays the page and always starts closed.
 */
(function () {
  var KEY = "esb.nav";
  var root = document.documentElement;

  function narrow() {
    return window.matchMedia("(max-width: 860px)").matches;
  }
  function stored() {
    try { return localStorage.getItem(KEY); } catch (e) { return null; }
  }
  function store(v) {
    try { localStorage.setItem(KEY, v); } catch (e) { /* private mode */ }
  }

  if (narrow() || stored() === "closed") root.classList.add("nav-closed");

  function isOpen() {
    return !root.classList.contains("nav-closed");
  }

  function sync() {
    var open = isOpen();
    document.querySelectorAll(".rail-toggle").forEach(function (b) {
      b.setAttribute("aria-expanded", open ? "true" : "false");
      b.setAttribute("data-tip", b.getAttribute(open ? "data-tip-open" : "data-tip-closed"));
    });
  }

  function setOpen(open) {
    root.classList.toggle("nav-closed", !open);
    if (!narrow()) store(open ? "open" : "closed");
    sync();
  }

  document.addEventListener("DOMContentLoaded", function () {
    var filter = document.getElementById("nav-filter");
    var panel = document.getElementById("nav-panel");
    sync();

    document.querySelectorAll("[data-nav-toggle]").forEach(function (b) {
      b.addEventListener("click", function () { setOpen(!isOpen()); });
    });

    // An icon that opens the panel closes it again when the panel is already
    // showing, so the same icon toggles — as in VS Code.
    document.querySelectorAll("[data-nav-open]").forEach(function (b) {
      b.addEventListener("click", function () {
        var what = b.getAttribute("data-nav-open");
        if (isOpen() && what === "aggregates") {
          setOpen(false);
          return;
        }
        setOpen(true);
        if (what === "search" && filter) {
          filter.focus();
          filter.select();
        } else {
          var active = panel.querySelector("#nav-aggregates a.active") || panel.querySelector("#nav-aggregates a");
          if (active) active.focus();
        }
      });
    });

    window.addEventListener("keydown", function (e) {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && (e.key === "b" || e.key === "B")) {
        e.preventDefault();
        setOpen(!isOpen());
      } else if (e.key === "Escape" && narrow() && isOpen()) {
        setOpen(false);
      }
    });

    // On a narrow screen the panel overlays the page: a click outside closes it.
    document.addEventListener("click", function (e) {
      if (!narrow() || !isOpen()) return;
      if (panel.contains(e.target) || e.target.closest(".rail")) return;
      setOpen(false);
    });

    if (!filter) return;
    var items = document.querySelectorAll("#nav-aggregates a[data-name]");
    filter.addEventListener("input", function () {
      var q = filter.value.trim().toLowerCase();
      items.forEach(function (a) {
        a.parentElement.hidden = q !== "" && a.getAttribute("data-name").toLowerCase().indexOf(q) < 0;
      });
    });
    filter.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        var first = Array.prototype.find.call(items, function (a) { return !a.parentElement.hidden; });
        if (first) window.location.href = first.href;
      } else if (e.key === "Escape") {
        filter.value = "";
        filter.dispatchEvent(new Event("input"));
      }
    });
  });
})();
