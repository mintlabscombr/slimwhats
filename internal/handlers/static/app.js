/*
 * Slimwhats manager UI — live event client (F-02/US-040 + F-04 audit).
 *
 * Self-routing: pages set `data-sse-mode` on a child of <main>:
 *   "list"   — instances home page (status badges, phone, last_seen)
 *   "detail" — per-instance page (status, phone/jid/lid, QR refresh)
 *   "audit"  — audit log page (F-04: prepend new rows from audit_entry)
 * Login / new-instance are no-ops.
 *
 * Wire protocol (matches internal/handlers/events.go):
 *   - "status"      : JSON {instance_id, status, phone?, jid?, lid?, last_seen?, connected_at?}
 *   - "qr_update"   : JSON {instance_id}  (client re-fetches the PNG)
 *   - "audit_entry" : JSON {timestamp, username, action, target_id?, source_ip, user_agent?}
 *
 * Standards-track only: EventSource, fetch, document.visibilityState.
 * No polyfills, no dependencies, no build step.
 */
(function () {
  'use strict';

  // --- Toast API (F-04) ------------------------------------------------
  // window.showToast(message, type, opts?) — append a toast to
  // #toast-stack, auto-dismiss after opts.duration ms (default 3000;
  // pass 0 to keep open until manually removed). Returns the toast
  // element. Pure DOM, no deps. Pages that want a one-shot toast on
  // load set `data-show-toast="message|type"` on <body> (or any
  // element); this script reads it once and clears the attribute.
  window.showToast = function (message, type, opts) {
    var stack = document.getElementById('toast-stack');
    if (!stack) return null;
    type = type || 'info';
    opts = opts || {};
    var ms = typeof opts.duration === 'number' ? opts.duration : 3000;
    var el = document.createElement('div');
    el.className = 'toast ' + type;
    el.textContent = message;
    el.setAttribute('role', 'status');
    el.setAttribute('aria-live', 'polite');
    stack.appendChild(el);
    if (ms > 0) {
      setTimeout(function () {
        el.style.transition = 'opacity 0.3s, transform 0.3s';
        el.style.opacity = '0';
        el.style.transform = 'translateX(20px)';
        setTimeout(function () { if (el.parentNode) el.parentNode.removeChild(el); }, 320);
      }, ms);
    }
    return el;
  };

  // Read a one-shot `data-show-toast` attribute from <body> and show
  // it. Format: "message|type" (type defaults to "info"). Cleared
  // after consumption so a refresh doesn't re-trigger.
  function consumeOneShotToast() {
    var raw = document.body && document.body.getAttribute('data-show-toast');
    if (!raw) return;
    document.body.removeAttribute('data-show-toast');
    var i = raw.indexOf('|');
    if (i < 0) { window.showToast(raw, 'info'); return; }
    window.showToast(raw.slice(0, i), raw.slice(i + 1) || 'info');
  }
  consumeOneShotToast();

  // The chrome wrapper is <main class="main-content">; pages that
  // need SSE hooks mark a child element with data-sse-mode.
  var main = document.querySelector('[data-sse-mode]');
  if (!main) return;
  var mode = main.dataset.sseMode;
  if (mode !== 'list' && mode !== 'detail' && mode !== 'audit') return;
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
    es.addEventListener('audit_entry', function (ev) {
      if (mode !== 'audit') return;
      var d; try { d = JSON.parse(ev.data); } catch (e) { return; }
      prependAuditRow(d);
    });
    return es;
  }

  function updateListCard(d) {
    var card = document.querySelector('[data-instance-id="' + d.instance_id + '"]');
    if (!card) return;
    var b = card.querySelector('.badge');
    var prev = b ? b.textContent : '';
    if (b && d.status) { b.className = 'badge ' + d.status; b.textContent = d.status; }
    var ph = card.querySelector('[data-field="phone"]');
    if (ph && 'phone' in d) ph.textContent = d.phone || '—';
    var ls = card.querySelector('[data-field="last_seen"]');
    if (ls && d.last_seen) ls.textContent = d.last_seen;
    // F-04: surface cross-instance transitions as a toast. The
    // operator is on the list page (not the detail page), so the
    // QR/identity updates aren't directly visible — a toast gives
    // them a "hey, that device finished pairing" signal without
    // making them click into the card.
    if (d.status === 'connected' && prev && prev !== 'connected') {
      var name = (card.querySelector('h3') || {}).textContent || d.instance_id;
      window.showToast('Instance "' + name + '" is now connected.', 'success');
    } else if (d.status === 'disconnected' && prev === 'connected') {
      var name = (card.querySelector('h3') || {}).textContent || d.instance_id;
      window.showToast('Instance "' + name + '" disconnected.', 'warn', { duration: 5000 });
    }
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
        startCountdown();
      })
      .catch(function () { /* next event will retry */ });
  }

  // "Next rotation in: 45s" countdown (F-02/US-041). Resets to 60s
  // every time the QR is refreshed, ticks down each second. Pure JS,
  // no extra endpoints.
  var qrTicker = null;
  function startCountdown() {
    var el = document.querySelector('[data-qr-countdown]');
    if (!el) return;
    if (qrTicker) clearInterval(qrTicker);
    var seconds = 60;
    el.textContent = 'Next rotation in: ' + seconds + 's';
    qrTicker = setInterval(function () {
      seconds--;
      if (seconds <= 0) {
        el.textContent = 'Rotating…';
        clearInterval(qrTicker);
        qrTicker = null;
      } else {
        el.textContent = 'Next rotation in: ' + seconds + 's';
      }
    }, 1000);
  }
  // Kick off the countdown on initial page load if the QR is visible.
  if (mode === 'detail' && document.getElementById('qr-img')) startCountdown();

  // --- Audit log live updates (F-04 / US-002) ---------------------------
  // On every `audit_entry` event, build a <tr> matching the layout the
  // Go template renders (Time / User / Action / Target / IP / UA) and
  // prepend it to the audit table. Keep the table at 100 rows max —
  // beyond that, drop the oldest. The new row gets a brief
  // "just-added" CSS class so the operator sees the change (the
  // animation is a pure CSS keyframe defined in src.css; no JS timer).
  //
  // XSS: the audit fields are operator-controlled (username,
  // user_agent, target_id). We render exclusively via textContent
  // (never innerHTML) and encodeURIComponent on the target_id href,
  // so the browser handles all escaping. No string interpolation
  // into the DOM.
  var auditMaxRows = 100;
  function prependAuditRow(d) {
    var tbody = document.querySelector('[data-audit-tbody]');
    if (!tbody) return;
    // First live row: drop the empty-state placeholder so the new
    // row is the only thing visible. (Multiple events can arrive
    // between checks; we re-check on every call so the placeholder
    // is removed at most once.)
    var empty = tbody.querySelector('[data-audit-empty]');
    if (empty && empty.parentNode) empty.parentNode.removeChild(empty);

    var tr = document.createElement('tr');
    tr.className = 'audit-row-just-added';

    var tdTime = document.createElement('td');
    tdTime.textContent = d.timestamp || '';
    tr.appendChild(tdTime);

    var tdUser = document.createElement('td');
    tdUser.textContent = d.username || '';
    tr.appendChild(tdUser);

    var tdAction = document.createElement('td');
    var code = document.createElement('code');
    code.textContent = d.action || '';
    tdAction.appendChild(code);
    tr.appendChild(tdAction);

    var tdTarget = document.createElement('td');
    if (d.target_id) {
      var a = document.createElement('a');
      a.href = '/admin/instances/' + encodeURIComponent(d.target_id);
      a.textContent = d.target_id;
      tdTarget.appendChild(a);
    } else {
      var dash = document.createElement('span');
      dash.className = 'muted';
      dash.textContent = '—';
      tdTarget.appendChild(dash);
    }
    tr.appendChild(tdTarget);

    var tdIP = document.createElement('td');
    tdIP.textContent = d.source_ip || '';
    tr.appendChild(tdIP);

    var tdUA = document.createElement('td');
    tdUA.className = 'muted text-xs max-w-[200px] truncate';
    tdUA.textContent = d.user_agent || '';
    tdUA.title = d.user_agent || '';
    tr.appendChild(tdUA);

    tbody.insertBefore(tr, tbody.firstChild);

    // Cap at auditMaxRows. When we add the 101st row the oldest
    // entry is no longer in the rendered window — the page only
    // shows the top 100 anyway (see AdminAuditPage).
    while (tbody.children.length > auditMaxRows) {
      tbody.removeChild(tbody.lastChild);
    }
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
