"use strict";

const $ = (id) => document.getElementById(id);
const base = window.location.pathname;
let busy = false;
let diagnosticsText = "";

const statusNames = {
  pending: "待领取",
  active: "有效",
  revoked: "已吊销",
};

async function bootstrap() {
  const response = await fetch(base + "api/config", {cache: "no-store"});
  const config = await response.json();
  const saved = localStorage.getItem("ezgamelan-admin-server");
  $("server").value = saved || config.default_server;
  bindEvents();
  await refresh();
  setInterval(() => {
    if (!document.hidden && !busy) refresh(true);
  }, 15000);
}

function bindEvents() {
  $("refresh").addEventListener("click", () => refresh());
  $("server").addEventListener("change", () => {
    localStorage.setItem("ezgamelan-admin-server", $("server").value.trim());
  });
  $("new-invite").addEventListener("click", () => {
    $("invite-panel").classList.remove("hidden");
    $("device-name").focus();
  });
  $("hide-invite").addEventListener("click", () => {
    $("invite-panel").classList.add("hidden");
    $("invite-result").classList.add("hidden");
    $("invite-link").value = "";
  });
  $("create-invite").addEventListener("click", createInvite);
  $("device-name").addEventListener("keydown", (event) => {
    if (event.key === "Enter") createInvite();
  });
  $("copy-invite").addEventListener("click", copyInvite);
  $("diagnostics").addEventListener("click", loadDiagnostics);
  $("export-diagnostics").addEventListener("click", exportDiagnostics);
  $("restart-openvpn").addEventListener("click", () => restartService(
    "restart_openvpn",
    "重启 OpenVPN 会暂时断开所有玩家，确认继续？",
  ));
  $("restart-enroll").addEventListener("click", () => restartService(
    "restart_enroll",
    "确认重启加入服务？正在领取链接的设备需要重试。",
  ));
  $("close-admin").addEventListener("click", closeAdmin);
  $("devices").addEventListener("click", handleDeviceAction);
}

async function call(action, name = "") {
  const server = $("server").value.trim();
  localStorage.setItem("ezgamelan-admin-server", server);
  const response = await fetch(base + "api/action", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({
      server,
      request: {
        protocol_version: 1,
        action,
        ...(name ? {name} : {}),
      },
    }),
  });
  let data;
  try {
    data = await response.json();
  } catch (_) {
    throw new Error("本机管理端返回了异常响应");
  }
  if (!response.ok || !data.ok) {
    throw new Error(data.error?.message || "管理操作失败");
  }
  if (data.warning) showNotice(data.warning, false);
  return data;
}

async function withBusy(task) {
  if (busy) return;
  busy = true;
  setButtonsDisabled(true);
  try {
    return await task();
  } catch (error) {
    showNotice(error.message || String(error), true);
  } finally {
    busy = false;
    setButtonsDisabled(false);
  }
}

function setButtonsDisabled(disabled) {
  for (const button of document.querySelectorAll("button")) {
    if (button.id === "export-diagnostics" && !diagnosticsText) continue;
    button.disabled = disabled;
  }
}

async function refresh(quiet = false) {
  return withBusy(async () => {
    const response = await call("snapshot");
    renderSnapshot(response.snapshot);
    if (response.snapshot.warning) showNotice(response.snapshot.warning, false);
    else if (!quiet) hideNotice();
  });
}

function renderSnapshot(snapshot) {
  $("public-host").textContent = snapshot.public_host;
  $("endpoint").textContent = snapshot.endpoint + " · " + snapshot.virtual_cidr;
  renderServiceState("openvpn-state", snapshot.openvpn_state);
  renderServiceState("enroll-state", snapshot.enroll_state);
  const online = snapshot.devices.filter((device) => device.online).length;
  $("online-count").textContent = String(online);
  $("device-count").textContent = `共 ${snapshot.devices.length} 台已登记`;
  $("updated-at").textContent = "上次刷新：" + formatDate(snapshot.generated_at);
  renderDevices(snapshot.devices);
}

function renderServiceState(id, state) {
  const names = {
    active: "运行正常",
    inactive: "未运行",
    failed: "运行失败",
    activating: "正在启动",
    deactivating: "正在停止",
    reloading: "正在重载",
    unknown: "状态未知",
  };
  const node = $(id);
  node.textContent = names[state] || state;
  node.className = "state " + state;
}

function renderDevices(devices) {
  const body = $("devices");
  body.replaceChildren();
  if (!devices.length) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 7;
    cell.className = "empty";
    cell.textContent = "暂无设备，可先生成加入链接。";
    row.appendChild(cell);
    body.appendChild(row);
    return;
  }
  for (const device of devices) {
    const row = document.createElement("tr");
    row.appendChild(cellWithDevice(device));
    row.appendChild(cellWithBadge(statusNames[device.status] || device.status, device.status));
    row.appendChild(cellWithBadge(device.online ? "在线" : "离线", device.online ? "online" : "offline"));
    row.appendChild(textCell(device.virtual_address || "—"));
    row.appendChild(textCell(device.online
      ? `↓ ${formatBytes(device.bytes_received)}  ↑ ${formatBytes(device.bytes_sent)}`
      : "—"));
    row.appendChild(textCell(formatDay(device.not_after)));
    row.appendChild(actionCell(device));
    body.appendChild(row);
  }
}

function cellWithDevice(device) {
  const cell = document.createElement("td");
  const name = document.createElement("div");
  name.className = "device-name";
  name.textContent = device.name;
  cell.appendChild(name);
  const detail = document.createElement("div");
  detail.className = "subtle";
  detail.textContent = device.online && device.connected_since
    ? "连接于 " + formatDate(device.connected_since)
    : "登记于 " + formatDay(device.created_at);
  cell.appendChild(detail);
  return cell;
}

function cellWithBadge(text, kind) {
  const cell = document.createElement("td");
  const badge = document.createElement("span");
  badge.className = "badge " + kind;
  badge.textContent = text;
  cell.appendChild(badge);
  return cell;
}

function textCell(text) {
  const cell = document.createElement("td");
  cell.textContent = text;
  return cell;
}

function actionCell(device) {
  const cell = document.createElement("td");
  cell.className = "right";
  const actions = document.createElement("div");
  actions.className = "row-actions";
  if (device.online) {
    actions.appendChild(actionButton("断开", "disconnect", device.name, ""));
  }
  if (device.status !== "revoked") {
    actions.appendChild(actionButton("吊销", "revoke", device.name, "danger"));
  }
  cell.appendChild(actions);
  return cell;
}

function actionButton(label, action, name, className) {
  const button = document.createElement("button");
  button.textContent = label;
  button.dataset.action = action;
  button.dataset.name = name;
  button.className = className;
  return button;
}

async function handleDeviceAction(event) {
  const button = event.target.closest("button[data-action]");
  if (!button) return;
  const action = button.dataset.action;
  const name = button.dataset.name;
  const message = action === "revoke"
    ? `确认吊销 ${name}？该设备需要新链接才能再次加入。`
    : `确认断开 ${name}？设备仍可使用现有凭据重新连接。`;
  if (!window.confirm(message)) return;
  await withBusy(async () => {
    const response = await call(action, name);
    showNotice(response.message || "操作完成", false);
    await refreshAfterAction();
  });
}

async function createInvite() {
  const name = $("device-name").value.trim();
  await withBusy(async () => {
    const response = await call("invite", name);
    $("invite-link").value = response.invite.link;
    $("invite-expiry").textContent = "有效期至：" + formatDate(response.invite.expires_at);
    $("invite-result").classList.remove("hidden");
    showNotice(response.message, false);
    await refreshAfterAction();
  });
}

async function copyInvite() {
  const link = $("invite-link").value;
  if (!link) return;
  try {
    await navigator.clipboard.writeText(link);
  } catch (_) {
    $("invite-link").select();
    document.execCommand("copy");
  }
  showNotice("加入链接已复制。", false);
}

async function loadDiagnostics() {
  await withBusy(async () => {
    const response = await call("diagnostics");
    diagnosticsText = response.diagnostics;
    $("diagnostic-output").textContent = diagnosticsText;
    $("export-diagnostics").disabled = false;
    showNotice(response.message, false);
  });
}

function exportDiagnostics() {
  if (!diagnosticsText) return;
  const blob = new Blob(["\ufeff", diagnosticsText], {type: "text/plain;charset=utf-8"});
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "EZGameLAN-server-diagnostics.txt";
  anchor.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

async function restartService(action, confirmation) {
  if (!window.confirm(confirmation)) return;
  await withBusy(async () => {
    const response = await call(action);
    showNotice(response.message, false);
    await new Promise((resolve) => setTimeout(resolve, 1000));
    await refreshAfterAction();
  });
}

async function refreshAfterAction() {
  const response = await call("snapshot");
  renderSnapshot(response.snapshot);
}

async function closeAdmin() {
  try {
    await fetch(base + "api/close", {method: "POST"});
  } finally {
    document.body.innerHTML = "<main><section class='panel'><div class='panel-heading'><h2>管理端已退出</h2></div></section></main>";
  }
}

function showNotice(text, error) {
  const notice = $("notice");
  notice.textContent = text;
  notice.className = "notice" + (error ? " error" : "");
}

function hideNotice() {
  $("notice").className = "notice hidden";
  $("notice").textContent = "";
}

function formatBytes(value) {
  if (!value) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB"];
  let number = Number(value);
  let index = 0;
  while (number >= 1024 && index < units.length - 1) {
    number /= 1024;
    index++;
  }
  return `${number.toFixed(index ? 1 : 0)} ${units[index]}`;
}

function formatDate(value) {
  return value ? new Date(value).toLocaleString("zh-CN", {hour12: false}) : "—";
}

function formatDay(value) {
  return value ? new Date(value).toLocaleDateString("zh-CN") : "—";
}

bootstrap().catch((error) => showNotice(error.message || String(error), true));
