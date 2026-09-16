const state = { connections: [], active: null, tables: [] };

const $ = (id) => document.getElementById(id);
const sqlEl = $("sql");
const grid = $("grid");
const statusEl = $("status");

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
    ...opts,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function setStatus(msg, ok = true) {
  statusEl.textContent = msg;
  statusEl.className = ok ? "ok" : "err";
}

function renderConns() {
  $("connList").innerHTML = state.connections.map((c) => `
    <div class="item ${state.active?.id === c.id ? "active" : ""}" data-id="${c.id}">
      <strong>${escapeHtml(c.name)}</strong><br />
      <span>${escapeHtml(c.driver)} ${c.readOnly ? "· RO" : ""}</span>
    </div>`).join("");
  $("connList").querySelectorAll(".item").forEach((el) => {
    el.onclick = () => selectConn(el.dataset.id);
  });
}

function renderTables() {
  $("tableList").innerHTML = state.tables.map((t) => `
    <div class="item" data-schema="${escapeHtml(t.schema || "")}" data-name="${escapeHtml(t.name)}">
      ${escapeHtml(t.schema ? t.schema + "." : "")}${escapeHtml(t.name)}
    </div>`).join("");
  $("tableList").querySelectorAll(".item").forEach((el) => {
    el.onclick = () => {
      const ident = el.dataset.schema ? `"${el.dataset.schema}"."${el.dataset.name}"` : `"${el.dataset.name}"`;
      sqlEl.value = `SELECT * FROM ${ident} LIMIT 100;`;
    };
  });
}

async function loadConns() {
  state.connections = await api("/api/connections");
  renderConns();
}

async function selectConn(id) {
  state.active = state.connections.find((c) => c.id === id);
  $("activeMeta").textContent = state.active ? `${state.active.name} · ${state.active.driver}` : "No connection";
  renderConns();
  try {
    state.tables = await api(`/api/connections/${id}/tables`);
    renderTables();
    setStatus(`Connected · ${state.tables.length} tables`);
  } catch (err) {
    state.tables = [];
    renderTables();
    setStatus(err.message, false);
  }
}

async function runQuery() {
  if (!state.active) return setStatus("Select a connection first", false);
  const sql = sqlEl.value.trim();
  if (!sql) return setStatus("SQL is empty", false);
  try {
    const res = await api(`/api/connections/${state.active.id}/query`, {
      method: "POST",
      body: JSON.stringify({ sql, allowWrite: $("allowWrite").checked }),
    });
    renderGrid(res);
    const extra = res.mutating ? `${res.rowsAffected || 0} affected` : `${res.rowCount} rows`;
    setStatus(`${extra} · ${res.durationMs}ms`);
  } catch (err) {
    grid.innerHTML = "";
    setStatus(err.message, false);
  }
}

function renderGrid(res) {
  const cols = res.columns || [];
  const rows = res.rows || [];
  if (!cols.length) {
    grid.innerHTML = `<tr><td>${res.mutating ? "Statement completed" : "No columns"}</td></tr>`;
    return;
  }
  grid.innerHTML = `<thead><tr>${cols.map((c) => `<th>${escapeHtml(c)}</th>`).join("")}</tr></thead>` +
    `<tbody>${rows.map((r) => `<tr>${cols.map((c) => `<td>${escapeHtml(fmt(r[c]))}</td>`).join("")}</tr>`).join("")}</tbody>`;
}

function fmt(v) {
  if (v === null || v === undefined) return "NULL";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

$("runBtn").onclick = runQuery;
sqlEl.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
    e.preventDefault();
    runQuery();
  }
});

$("newConn").onclick = () => $("connDialog").showModal();
$("connForm").addEventListener("close", async (e) => {
  if ($("connForm").returnValue !== "save") return;
  const fd = new FormData($("connForm"));
  const body = {
    name: fd.get("name"),
    driver: fd.get("driver"),
    host: fd.get("host"),
    port: Number(fd.get("port") || 0),
    user: fd.get("user"),
    password: fd.get("password"),
    database: fd.get("database"),
    dsn: fd.get("dsn"),
    readOnly: fd.has("readOnly"),
  };
  try {
    const saved = await api("/api/connections", { method: "POST", body: JSON.stringify(body) });
    await loadConns();
    await selectConn(saved.id);
  } catch (err) {
    setStatus(err.message, false);
  }
});

loadConns().catch((err) => setStatus(err.message, false));
