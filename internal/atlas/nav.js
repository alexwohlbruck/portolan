// Shared workbench nav: view switcher, feed picker, run buttons, live log.
// Appended to every atlas page by the server.
(function () {
  const FEED = new URLSearchParams(location.search).get('feed') || '5';
  const here = location.pathname;
  const bar = document.createElement('div');
  bar.style.cssText =
    'position:fixed;top:8px;left:50%;transform:translateX(-50%);z-index:1200;' +
    'display:flex;gap:8px;align-items:center;background:#15151acc;' +
    'backdrop-filter:blur(6px);color:#ddd;font:13px system-ui;' +
    'padding:7px 12px;border-radius:12px;box-shadow:0 2px 12px #0006';
  const link = (href, label) => {
    const a = document.createElement('a');
    a.href = href + '?feed=' + FEED;
    a.textContent = label;
    const active = here === href;
    a.style.cssText =
      'color:' + (active ? '#fff' : '#9ab') + ';text-decoration:none;' +
      'padding:3px 10px;border-radius:8px;background:' +
      (active ? '#2c2c34' : 'transparent');
    return a;
  };
  bar.append(link('/map', 'map'), link('/sketch', 'sketch'));

  const feedSel = document.createElement('select');
  feedSel.style.cssText = 'background:#222;color:#ddd;border:1px solid #333;' +
    'border-radius:8px;padding:3px 6px';
  fetch('/api/config').then(r => r.json()).then(cfg => {
    for (const [id, f] of Object.entries(cfg.feeds || {})) {
      const o = document.createElement('option');
      o.value = id; o.textContent = f.name || id;
      if (id === FEED) o.selected = true;
      feedSel.append(o);
    }
  });
  feedSel.onchange = () => {
    location.href = here + '?feed=' + feedSel.value;
  };
  bar.append(feedSel);

  const logBox = document.createElement('pre');
  logBox.style.cssText =
    'position:fixed;bottom:10px;left:50%;transform:translateX(-50%);' +
    'z-index:1200;max-width:80vw;max-height:34vh;overflow:auto;' +
    'background:#15151add;color:#cde;font:11px/1.5 ui-monospace,monospace;' +
    'padding:10px 14px;border-radius:10px;display:none;margin:0;' +
    'box-shadow:0 2px 12px #0006;white-space:pre-wrap';
  document.addEventListener('DOMContentLoaded', () => document.body.append(logBox));

  let polling = null;
  const btn = (label, cmd) => {
    const b = document.createElement('button');
    b.textContent = label;
    b.style.cssText = 'background:#2c4a6e;color:#fff;border:0;border-radius:8px;' +
      'padding:4px 12px;cursor:pointer;font:600 12px system-ui';
    b.onclick = async () => {
      const r = await fetch('/api/run?cmd=' + cmd + '&feed=' + FEED, { method: 'POST' });
      if (r.status === 409) { alert('a run is already in progress'); return; }
      logBox.style.display = 'block';
      logBox.textContent = '… ' + cmd + ' started\n';
      clearInterval(polling);
      polling = setInterval(async () => {
        const st = await (await fetch('/api/run/status')).json();
        logBox.textContent = '· ' + st.cmd + (st.running ? ' (running)\n' : '\n')
          + (st.log || []).join('\n');
        logBox.scrollTop = logBox.scrollHeight;
        if (st.done) {
          clearInterval(polling);
          if (st.ok && window.portolanReload) window.portolanReload();
        }
      }, 700);
    };
    return b;
  };
  bar.append(btn('⟳ rebuild', 'chart'), btn('⚖ score', 'sound'));

  const hide = document.createElement('button');
  hide.textContent = '×log';
  hide.style.cssText = 'background:#333;color:#aaa;border:0;border-radius:8px;' +
    'padding:4px 8px;cursor:pointer;font:12px system-ui';
  hide.onclick = () => { logBox.style.display = 'none'; };
  bar.append(hide);

  if (document.body) document.body.append(bar);
  else document.addEventListener('DOMContentLoaded', () => document.body.append(bar));
})();
