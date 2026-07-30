/*
 * Slimwhats manager UI — live event client (F-02/US-040).
 *
 * Self-routing: pages set `data-sse-mode` on <main> ("list" or "detail");
 * the script only acts when it finds a known mode. Login / new / audit
 * are no-ops.
 *
 * Wire protocol (matches internal/handlers/events.go):
 *   - "status"    : JSON {instance_id, status, phone?, jid?, lid?, last_seen?, connected_at?}
 *   - "qr_update" : JSON {instance_id}  (client re-fetches the PNG)
 *
 * Standards-track only: EventSource, fetch, document.visibilityState.
 * No polyfills, no dependencies, no build step.
 */
(function () {
  'use strict';
  // The chrome wrapper is <main class="main-content">; pages that
  // need SSE hooks mark a child element with data-sse-mode.
  var main = document.querySelector('[data-sse-mode]');
  if (!main) return;
  var mode = main.dataset.sseMode;
  if (mode !== 'list' && mode !== 'detail') return;
  var instanceId = main.dataset.instanceId || '';
  if (typeof EventSource === 'undefined') return;

  var es = null;
  var paused = document.hidden;
  var connectedShown = false;

  function connect() {
    if (es && es.readyState !== EventSource.CLOSED) return es;
    es = new EventSource('/admin/api/events');
    es.addEventListener('status', function (ev) {
      if (paused) return;
      var d; try { d = JSON.parse(ev.data); } catch (e) { return; }
      mode === 'list' ? updateListCard(d) : updateDetail(d);
    });
    es.addEventListener('qr_update', function (ev) {
      if (paused || mode !== 'detail') return;
      var d; try { d = JSON.parse(ev.data); } catch (e) { return; }
      if (d.instance_id === instanceId) refreshQR();
    });
    return es;
  }

  function updateListCard(d) {
    var card = document.querySelector('[data-instance-id="' + d.instance_id + '"]');
    if (!card) return;
    var b = card.querySelector('.badge');
    if (b && d.status) { b.className = 'badge ' + d.status; b.textContent = d.status; }
    var ph = card.querySelector('[data-field="phone"]');
    if (ph && 'phone' in d) ph.textContent = d.phone || '—';
    var ls = card.querySelector('[data-field="last_seen"]');
    if (ls && d.last_seen) ls.textContent = d.last_seen;
  }

  function updateDetail(d) {
    if (d.instance_id !== instanceId) return;
    var b = document.querySelector('.badge');
    if (b && d.status) { b.className = 'badge ' + d.status; b.textContent = d.status; }
    ['phone', 'jid', 'lid', 'connected_at', 'last_seen'].forEach(function (k) {
      if (!(k in d) || d[k] == null) return;
      var el = document.querySelector('[data-row="' + k + '"]');
      if (el) el.textContent = d[k];
    });
    if (d.status === 'connected' && !connectedShown) {
      connectedShown = true;
      var slot = document.querySelector('[data-alert-slot]');
      if (slot) slot.innerHTML = '<div class="alert ok">Connected ✓ — reloading in 2s</div>';
      setTimeout(function () { location.reload(); }, 2000);
    }
  }

  function refreshQR() {
    var img = document.getElementById('qr-img');
    if (!img) return;
    fetch('/admin/api/instances/' + encodeURIComponent(instanceId) + '/qr')
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (j) {
        if (!j || !j.qr) return;
        img.style.opacity = '0.3';
        setTimeout(function () { img.src = j.qr; img.style.opacity = '1'; }, 200);
      })
      .catch(function () { /* next event will retry */ });
  }

  // Page Visibility: close SSE when hidden, reopen + re-fetch QR when
  // visible. EventSource auto-reconnects but takes seconds; we close
  // explicitly so the server can free the subscription immediately.
  document.addEventListener('visibilitychange', function () {
    paused = document.hidden;
    if (paused) { if (es) { es.close(); es = null; } }
    else {
      connect();
      if (mode === 'detail' && document.getElementById('qr-img')) refreshQR();
    }
  });

  connect();
})();
