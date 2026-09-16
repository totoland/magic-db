const state = { connections: [], active: null, tables: [], hintTables: {}, editor: null };

const $ = (id) => document.getElementById(id);
const grid = $("grid");
const statusEl = $("status");

function sqlText() {
  return state.editor ? state.editor.getValue() : $("sql").value;
}

function setSql(text) {
  if (state.editor) state.editor.setValue(text);
  else $("sql").value = text;
}

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
  statusEl.className = `px-4 py-2 text-sm ${ok ? "text-emerald-400" : "text-rose-400"}`;
}

function isSimpleIdent(name) {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(name || "");
}

function quoteIdent(name, driver) {
  if (!name) return "";
  if (isSimpleIdent(name)) return name;
  if (driver === "mysql") return "`" + String(name).replaceAll("`", "``") + "`";
  if (driver === "mssql") return "[" + String(name).replaceAll("]", "]]") + "]";
  return '"' + String(name).replaceAll('"', '""') + '"';
}

function tableRef(schema, name, driver) {
  const skipSchema = !schema || schema === "main" || (driver === "postgres" && schema === "public") || (driver === "mssql" && schema === "dbo");
  const table = quoteIdent(name, driver);
  if (skipSchema) return table;
  return `${quoteIdent(schema, driver)}.${table}`;
}

function itemClass(active) {
  return `w-full text-left rounded-lg px-3 py-2 text-sm border ${active ? "border-blue-500 bg-blue-500/10 text-white" : "border-transparent hover:bg-zinc-800 text-zinc-300"}`;
}

function renderConns() {
  $("connList").innerHTML = state.connections.map((c) => `
    <button type="button" class="${itemClass(state.active?.id === c.id)}" data-id="${c.id}">
      <div class="font-medium truncate">${escapeHtml(c.name)}</div>
      <div class="text-xs text-zinc-500">${escapeHtml(c.driver)}${c.readOnly ? " · RO" : ""}</div>
    </button>`).join("");
  $("connList").querySelectorAll("button").forEach((el) => {
    el.onclick = () => {
      selectConn(el.dataset.id);
      closeSidebar();
    };
  });
}

function renderTables() {
  $("tableList").innerHTML = state.tables.map((t) => `
    <button type="button" class="${itemClass(false)}" data-schema="${escapeHtml(t.schema || "")}" data-name="${escapeHtml(t.name)}">
      <div class="truncate">${escapeHtml(t.schema && t.schema !== "main" && t.schema !== "public" ? t.schema + "." : "")}${escapeHtml(t.name)}</div>
    </button>`).join("");
  $("tableList").querySelectorAll("button").forEach((el) => {
    el.onclick = () => {
      const driver = state.active?.driver;
      const ident = tableRef(el.dataset.schema, el.dataset.name, driver);
      setSql(`SELECT * FROM ${ident} LIMIT 100;`);
      if (state.editor) state.editor.focus();
      closeSidebar();
    };
  });
}

function buildHints(columns) {
  const tables = {};
  for (const col of columns || []) {
    const full = tableRef(col.schema, col.table, state.active?.driver);
    const short = quoteIdent(col.table, state.active?.driver);
    if (!tables[full]) tables[full] = [];
    if (!tables[short]) tables[short] = [];
    tables[full].push(col.name);
    if (!tables[short].includes(col.name)) tables[short].push(col.name);
  }
  state.hintTables = tables;
  if (state.editor) {
    state.editor.setOption("hintOptions", {
      tables,
      defaultTable: Object.keys(tables)[0],
      completeSingle: false,
    });
  }
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
    const [tables, columns] = await Promise.all([
      api(`/api/connections/${id}/tables`),
      api(`/api/connections/${id}/schema`).catch(() => []),
    ]);
    state.tables = tables;
    renderTables();
    buildHints(columns);
    setStatus(`Connected · ${state.tables.length} tables · Ctrl-Space autocomplete`);
  } catch (err) {
    state.tables = [];
    renderTables();
    setStatus(err.message, false);
  }
}

async function runQuery() {
  if (!state.active) return setStatus("Select a connection first", false);
  const sql = sqlText().trim();
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
    grid.innerHTML = `<tbody><tr><td class="px-4 py-3 text-zinc-500">${res.mutating ? "Statement completed" : "No columns"}</td></tr></tbody>`;
    return;
  }
  grid.innerHTML = `<thead class="sticky top-0 bg-ink-900"><tr>${cols.map((c) => `<th class="px-3 py-2 text-left text-xs font-medium text-zinc-500 border-b border-zinc-800 whitespace-nowrap">${escapeHtml(c)}</th>`).join("")}</tr></thead>` +
    `<tbody>${rows.map((r) => `<tr class="hover:bg-zinc-900">${cols.map((c) => `<td class="px-3 py-2 border-b border-zinc-800 whitespace-nowrap font-mono text-[12px]">${escapeHtml(fmt(r[c]))}</td>`).join("")}</tr>`).join("")}</tbody>`;
}

function fmt(v) {
  if (v === null || v === undefined) return "NULL";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function initEditor() {
  state.editor = CodeMirror.fromTextArea($("sql"), {
    mode: "text/x-pgsql",
    lineNumbers: true,
    lineWrapping: true,
    extraKeys: {
      "Ctrl-Space": "autocomplete",
      "Cmd-Enter": runQuery,
      "Ctrl-Enter": runQuery,
    },
    hintOptions: { tables: state.hintTables, completeSingle: false },
  });
  state.editor.setSize("100%", "100%");
  setTimeout(() => state.editor.refresh(), 50);
  state.editor.on("inputRead", (cm, change) => {
    if (change.origin === "complete") return;
    const ch = change.text[0];
    if (!ch || !/[A-Za-z0-9_.]/.test(ch)) return;
    CodeMirror.commands.autocomplete(cm, null, { completeSingle: false });
  });
}

function openSidebar() {
  $("sidebar").classList.remove("-translate-x-full");
  $("sidebarOverlay").classList.remove("hidden");
}

function closeSidebar() {
  $("sidebar").classList.add("-translate-x-full");
  $("sidebarOverlay").classList.add("hidden");
}

$("openSidebar").onclick = openSidebar;
$("closeSidebar").onclick = closeSidebar;
$("sidebarOverlay").onclick = closeSidebar;
$("runBtn").onclick = runQuery;
window.addEventListener("resize", () => {
  if (window.innerWidth >= 768) closeSidebar();
  if (state.editor) state.editor.refresh();
});

function connectionFromForm() {
  const fd = new FormData($("connForm"));
  return {
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
}

$("newConn").onclick = () => {
  $("testResult").textContent = "";
  $("testResult").className = "text-sm min-h-5";
  $("connForm").reset();
  $("connDialog").showModal();
};

$("cancelConn").onclick = () => $("connDialog").close();

$("testConn").onclick = async (e) => {
  e.preventDefault();
  const result = $("testResult");
  result.textContent = "Testing...";
  result.className = "text-sm min-h-5 text-zinc-400";
  try {
    await api("/api/test-connection", { method: "POST", body: JSON.stringify(connectionFromForm()) });
    result.textContent = "Connection OK";
    result.className = "text-sm min-h-5 text-emerald-400";
  } catch (err) {
    result.textContent = err.message;
    result.className = "text-sm min-h-5 text-rose-400";
  }
};

$("connForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const result = $("testResult");
  const saveBtn = $("saveConn");
  saveBtn.disabled = true;
  result.textContent = "Saving...";
  result.className = "text-sm min-h-5 text-zinc-400";
  try {
    const saved = await api("/api/connections", { method: "POST", body: JSON.stringify(connectionFromForm()) });
    result.textContent = "Saved";
    result.className = "text-sm min-h-5 text-emerald-400";
    $("connDialog").close();
    await loadConns();
    await selectConn(saved.id);
    setStatus(`Saved ${saved.name}`);
  } catch (err) {
    result.textContent = err.message;
    result.className = "text-sm min-h-5 text-rose-400";
    setStatus(err.message, false);
  } finally {
    saveBtn.disabled = false;
  }
});

initEditor();
loadConns().catch((err) => setStatus(err.message, false));
