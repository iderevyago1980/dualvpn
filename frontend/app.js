// DualVPN — логика фронтенда: вызовы Go-биндингов Wails и live-обновление UI.
//
// Биндинги: структура App живёт в Go-пакете internal/ui, поэтому стандартный
// путь — window.go.ui.App; window.go.main.App оставлен как fallback
// на случай переноса App в пакет main.
// События реального времени: window.runtime.EventsOn("tunnel:event" | "tunnel:2fa" | "log").
'use strict';

/** Go-биндинги (null, если страница открыта вне Wails). */
let App = null;

/** Конфигурация туннелей (config.Tunnel из Go, поля с большой буквы). */
let tunnels = [];
/** Полная конфигурация (для сохранения Mode.Preferred). */
let fullConfig = null;
/**
 * Слепок конфигурации, с которой работает бэкенд. Нужен, чтобы отличить
 * правки формы от сохранённого состояния: подключение выполняется по
 * сохранённой конфигурации, а не по тому, что набрано в полях.
 */
let savedSnapshot = '';
/** Имя (ID) выбранного в сайдбаре туннеля. */
let activeId = null;
/** Текущий режим работы: tun | socks5. */
let currentMode = '';
/** Статус туннелей: имя → { connected, connecting, mode, seconds, timer }. */
const status = {};
/** DOM-элементы сайдбара: имя → { item, dot }. */
const sidebarEls = {};
/** Туннель, ожидающий ввода 2FA-кода. */
let twofaTunnelId = null;

// ---------------------------------------------------------------- инициализация

window.addEventListener('DOMContentLoaded', async () => {
  App = (window.go && ((window.go.ui && window.go.ui.App) || (window.go.main && window.go.main.App))) || null;

  if (!App || !window.runtime) {
    logLocal('err', 'Wails runtime недоступен — страница открыта вне приложения');
    return;
  }

  subscribeEvents();

  try {
    fullConfig = await App.GetConfig();
    tunnels = await App.GetTunnels() || [];
    savedSnapshot = formSnapshot(); // отправная точка: форма = конфиг бэкенда
    tunnels.forEach(t => { status[t.Name] = { connected: false, connecting: false, mode: '', seconds: 0, timer: null }; });

    renderSidebar();
    await refreshModePill();

    // Восстановить журнал и фактические статусы (например, после смены режима).
    const logs = await App.GetLogs() || [];
    logs.forEach(appendLog);
    for (const t of tunnels) {
      const st = await App.GetTunnelStatus(t.Name);
      if (st && st.connected) markConnected(t.Name, st.mode);
    }

    if (tunnels.length) selectTunnel(tunnels[0].Name); else showNoTunnels();

    const version = await App.Version();
    if (version) document.getElementById('appVersion').textContent = 'v' + version;

    await refreshPAC();

    setFooter('Готово');
  } catch (e) {
    logLocal('err', 'инициализация UI: ' + e);
  }
});

/** Подписка на события бэкенда. */
function subscribeEvents() {
  window.runtime.EventsOn('tunnel:event', onTunnelEvent);
  window.runtime.EventsOn('tunnel:2fa', showTwoFA);
  window.runtime.EventsOn('log', appendLog);
}

// ---------------------------------------------------------------- события туннелей

/** Обработка события туннеля: {tunnelId, type, message}. */
function onTunnelEvent(ev) {
  const st = status[ev.tunnelId];
  if (!st) return;

  switch (ev.type) {
    case 'connected':
      markConnected(ev.tunnelId, currentMode);
      break;
    case 'disconnected':
    case 'error':
      markDisconnected(ev.tunnelId);
      break;
    case '2fa_required':
      // Дот остаётся в состоянии «подключение» — ждём код (модал откроет tunnel:2fa).
      break;
  }
  updateFooterCounts();
}

/** Перевод туннеля в состояние «подключён» + запуск таймера аптайма. */
function markConnected(id, mode) {
  const st = status[id];
  if (!st || st.connected) return;
  st.connected = true;
  st.connecting = false;
  st.mode = mode || currentMode;
  st.seconds = 0;
  clearInterval(st.timer);
  st.timer = setInterval(() => {
    st.seconds++;
    if (id === activeId) renderStats();
  }, 1000);
  setDot(id, 'on');
  if (id === activeId) { renderStats(); updateConnectBtn(); }
}

/** Перевод туннеля в состояние «отключён» + остановка таймера. */
function markDisconnected(id) {
  const st = status[id];
  if (!st) return;
  st.connected = false;
  st.connecting = false;
  st.mode = '';
  clearInterval(st.timer);
  st.timer = null;
  st.seconds = 0;
  setDot(id, 'off');
  if (id === activeId) { renderStats(); updateConnectBtn(); }
}

// ---------------------------------------------------------------- сайдбар

/** Построение списка туннелей в сайдбаре из конфигурации. */
function renderSidebar() {
  const box = document.getElementById('tunnelList');
  box.textContent = '';
  Object.keys(sidebarEls).forEach(k => delete sidebarEls[k]);

  for (const t of tunnels) {
    const item = document.createElement('div');
    item.className = 'tunnel-item';
    item.addEventListener('click', () => selectTunnel(t.Name));

    const row = document.createElement('div');
    row.className = 'tunnel-row';
    const dot = document.createElement('span');
    dot.className = 'status-dot dot-off';
    const label = document.createElement('span');
    label.className = 'tunnel-label';
    label.textContent = t.Name;
    row.append(dot, label);

    const meta = document.createElement('div');
    meta.className = 'tunnel-meta';
    meta.textContent = t.Endpoint;

    item.append(row, meta);
    box.appendChild(item);
    sidebarEls[t.Name] = { item, dot };
  }
}

/** Выбор туннеля: подсветка в сайдбаре + заполнение формы. */
function selectTunnel(id) {
  const t = tunnels.find(x => x.Name === id);
  if (!t) return;
  activeId = id;

  for (const [name, els] of Object.entries(sidebarEls)) {
    els.item.classList.toggle('active', name === id);
  }

  document.getElementById('tunnelTitle').textContent = t.Name;
  document.getElementById('tunnelSubtitle').textContent =
    `${t.Endpoint} · AnyConnect (SSL/TLS) · Group: ${t.Group || '—'}`;

  document.getElementById('fName').value = t.Name || '';
  document.getElementById('fEndpoint').value = t.Endpoint || '';
  document.getElementById('fGroup').value = t.Group || '';
  document.getElementById('fUser').value = t.Username || '';
  document.getElementById('fPass').value = t.Password || '';
  document.getElementById('fSocks').value = t.SocksPort || 1080;
  document.getElementById('fTun').value = t.TunName || '';

  renderRoutes();
  renderStats();
  updateConnectBtn();
}

// ------------------------------------------------- добавление и удаление

/**
 * Кнопка «+ Добавить туннель»: создаёт запись с уникальным именем и
 * свободным SOCKS-портом и открывает её в форме. В конфигурацию туннель
 * попадает только после «Сохранить» — до этого он живёт в интерфейсе,
 * поэтому незаполненный адрес сервера ничего не ломает.
 */
function addTunnel() {
  const name = uniqueTunnelName();
  tunnels.push({
    Name: name,
    Endpoint: '',
    Group: '',
    Username: '',
    Password: '',
    SocksPort: nextSocksPort(),
    TunName: 'dualvpn' + tunnels.length,
    Routes: [],
  });
  status[name] = { connected: false, connecting: false, mode: '', seconds: 0, timer: null };

  renderSidebar();
  selectTunnel(name);
  setFooter('Новый туннель добавлен — укажите адрес сервера и нажмите «Сохранить»');
  document.getElementById('fEndpoint').focus();
}

/** Имя вида «Туннель N», которого ещё нет в списке. */
function uniqueTunnelName() {
  const taken = new Set(tunnels.map(t => t.Name));
  for (let i = tunnels.length + 1; ; i++) {
    const name = 'Туннель ' + i;
    if (!taken.has(name)) return name;
  }
}

/** Свободный локальный порт SOCKS5: занятые порты бэкенд отвергает. */
function nextSocksPort() {
  const taken = new Set(tunnels.map(t => t.SocksPort));
  let port = 1080;
  while (taken.has(port)) port++;
  return port;
}

/** Запрос подтверждения на удаление активного туннеля. */
function askDeleteTunnel() {
  if (!activeId) return;
  document.getElementById('deleteName').textContent = activeId;
  document.getElementById('deleteOverlay').classList.remove('hidden');
}

/** Закрытие запроса на удаление. */
function hideDeleteTunnel() {
  document.getElementById('deleteOverlay').classList.add('hidden');
}

/**
 * Удаление туннеля: убираем из списка и сразу сохраняем конфигурацию —
 * иначе удалённый туннель вернулся бы при следующем чтении конфига.
 */
async function deleteTunnel() {
  hideDeleteTunnel();
  const idx = tunnels.findIndex(t => t.Name === activeId);
  if (idx < 0) return;

  const removed = tunnels[idx].Name;
  // Остановить таймер аптайма удаляемого туннеля, иначе он остался бы
  // тикать по исчезнувшей записи.
  const st = status[removed];
  if (st && st.timer) clearInterval(st.timer);
  delete status[removed];
  tunnels.splice(idx, 1);

  activeId = tunnels.length ? tunnels[Math.min(idx, tunnels.length - 1)].Name : null;
  renderSidebar();
  if (activeId) {
    selectTunnel(activeId);
  } else {
    showNoTunnels();
  }

  if (await saveConfig()) setFooter('Туннель «' + removed + '» удалён');
}

/** Пустое состояние: в конфигурации не осталось ни одного туннеля. */
function showNoTunnels() {
  document.getElementById('tunnelTitle').textContent = 'Нет туннелей';
  document.getElementById('tunnelSubtitle').textContent =
    'Добавьте туннель кнопкой «+ Добавить туннель» слева';
  for (const id of ['fName', 'fEndpoint', 'fGroup', 'fUser', 'fPass', 'fTun']) {
    document.getElementById(id).value = '';
  }
  document.getElementById('fSocks').value = '';
  renderRoutes();
  renderStats();
  updateConnectBtn();
}

/** Точка статуса туннеля в сайдбаре: state = on | off | connecting. */
function setDot(id, state) {
  const els = sidebarEls[id];
  if (els) els.dot.className = 'status-dot dot-' + state;
}

// ---------------------------------------------------------------- действия

/** Кнопка «Подключить/Отключить» для активного туннеля. */
async function toggleTunnel() {
  if (!activeId || !App) return;
  const st = status[activeId];
  try {
    if (st.connected || st.connecting) {
      await App.DisconnectTunnel(activeId);
      // Итоговое состояние придёт событием disconnected.
    } else {
      // Бэкенд подключается по сохранённой конфигурации — сперва применяем форму.
      if (!await ensureFormApplied()) return;
      st.connecting = true;
      setDot(activeId, 'connecting');
      updateConnectBtn();
      await App.ConnectTunnel(activeId);
    }
  } catch (e) {
    markDisconnected(activeId);
    logLocal('err', String(e));
  }
}

/** Кнопка «Подключить все». */
async function connectAll() {
  if (!App) return;
  if (!await ensureFormApplied()) return; // подключаемся тем, что показано в форме
  for (const t of tunnels) {
    const st = status[t.Name];
    if (!st.connected && !st.connecting) {
      st.connecting = true;
      setDot(t.Name, 'connecting');
    }
  }
  updateConnectBtn();
  try {
    await App.ConnectAll();
  } catch (e) {
    logLocal('err', String(e));
  }
}

/** Кнопка «Отключить все». */
async function disconnectAll() {
  if (!App) return;
  try {
    await App.DisconnectAll();
  } catch (e) {
    logLocal('err', String(e));
  }
}

/**
 * Кнопка «Сменить режим»: tun ↔ socks5 (бэкенд останавливает туннели).
 *
 * Режим TUN требует прав администратора. Обычный (per-user) запуск их не
 * имеет, поэтому вместо молчаливого отказа предлагаем перезапуск с
 * повышением прав — иначе кнопка выглядела бы неисправной: отказ уходил
 * только в журнал, а в интерфейсе не менялось ничего.
 */
async function switchMode() {
  if (!App) return;
  const target = currentMode === 'tun' ? 'socks5' : 'tun';
  if (target === 'tun' && !(await App.IsAdmin())) {
    showElevate();
    return;
  }
  try {
    await App.SwitchMode(target);
    tunnels.forEach(t => markDisconnected(t.Name));
    await refreshModePill();
    await refreshPAC(); // PAC живёт только в режиме SOCKS5
    setFooter('Режим переключён: ' + target.toUpperCase());
  } catch (e) {
    const msg = String(e);
    // Права могли пропасть между проверкой и вызовом (или режим задан
    // в конфиге) — предлагаем перезапуск и в этом случае.
    if (msg.includes(await App.NeedsAdminMessage())) {
      showElevate();
      return;
    }
    setFooter('Не удалось сменить режим: ' + msg);
    logLocal('err', msg);
  }
}

/** Показать запрос на перезапуск с правами администратора. */
function showElevate() {
  document.getElementById('elevateOverlay').classList.remove('hidden');
}

/** Закрыть запрос на перезапуск. */
function hideElevate() {
  document.getElementById('elevateOverlay').classList.add('hidden');
  setFooter('Режим TUN недоступен без прав администратора');
}

/**
 * Перезапуск с повышением прав: бэкенд запускает новый экземпляр (UAC) и
 * закрывает текущий. Отказ в диалоге UAC возвращается ошибкой — окно
 * закрываем, приложение продолжает работать в SOCKS5.
 */
async function restartAsAdmin() {
  document.getElementById('elevateOverlay').classList.add('hidden');
  try {
    await App.RestartAsAdmin();
    setFooter('Перезапуск с правами администратора…');
  } catch (e) {
    setFooter('Перезапуск не выполнен: ' + e);
    logLocal('err', String(e));
  }
}

/** Обновление пилюли режима в шапке. */
async function refreshModePill() {
  currentMode = await App.GetMode();
  const admin = await App.IsAdmin();
  const pill = document.getElementById('modePill');
  if (currentMode === 'tun') {
    pill.textContent = 'TUN · Admin';
    pill.className = 'mode-pill';
  } else {
    pill.textContent = 'SOCKS5 · ' + (admin ? 'Admin' : 'No-Admin');
    pill.className = 'mode-pill socks';
  }
  renderStats();
}

// ---------------------------------------------------------------- форма и конфиг

/** Считывание полей формы в конфигурацию активного туннеля. */
function collectForm() {
  const t = tunnels.find(x => x.Name === activeId);
  if (!t) return;

  // Имя туннеля — его идентификатор: под ним живут статусы, элементы
  // сайдбара и регистрация в бэкенде. При переименовании состояние нужно
  // перенести на новое имя, иначе туннель «потеряется» в интерфейсе.
  const newName = document.getElementById('fName').value.trim();
  if (newName && newName !== t.Name) {
    status[newName] = status[t.Name] || { connected: false, connecting: false, mode: '', seconds: 0, timer: null };
    delete status[t.Name];
    t.Name = newName;
    activeId = newName;
    renderSidebar();
    document.getElementById('tunnelTitle').textContent = newName;
    setDot(newName, status[newName].connected ? 'on' : 'off');
  }

  t.Endpoint = document.getElementById('fEndpoint').value.trim();
  t.Group = document.getElementById('fGroup').value.trim();
  t.Username = document.getElementById('fUser').value.trim();
  t.Password = document.getElementById('fPass').value;
  t.SocksPort = parseInt(document.getElementById('fSocks').value, 10) || 0;
  t.TunName = document.getElementById('fTun').value.trim();
}

/**
 * Показывает адрес PAC-файла в подвале. PAC поднимается только в режиме
 * SOCKS5: он нужен, чтобы браузер сам выбирал туннель по домену. В
 * TUN-режиме трафик идёт по маршрутам системы, и блок скрыт.
 */
async function refreshPAC() {
  if (!App) return;
  const line = document.getElementById('pacLine');
  const block = document.getElementById('pacBlock');
  let url = '';
  try {
    url = await App.PACURL();
  } catch (e) {
    logLocal('err', String(e));
  }
  document.getElementById('pacUrl').textContent = url || '';
  line.classList.toggle('hidden', !url);
  block.classList.toggle('hidden', !url);
  await updateProxyBtn(!!url);
}

/**
 * Обновляет кнопку «Применить/Снять прокси». Кнопка видна только когда есть
 * PAC (режим SOCKS5); подпись отражает, направлен ли уже системный прокси в
 * туннели.
 */
async function updateProxyBtn(hasPAC) {
  const btn = document.getElementById('proxyBtn');
  if (!btn) return;
  btn.classList.toggle('hidden', !hasPAC);
  if (!hasPAC || !App) return;
  let applied = false;
  try {
    applied = await App.SystemProxyApplied();
  } catch (e) {
    logLocal('err', String(e));
  }
  btn.textContent = applied ? 'Снять прокси' : 'Применить прокси';
  btn.classList.toggle('btn-primary', !applied);
  btn.classList.toggle('btn-ghost', applied);
}

/**
 * Кнопка «Применить/Снять прокси»: направляет системный прокси Windows на
 * раздаваемый PAC (или убирает его). В отличие от копирования адреса вручную,
 * браузеры сразу начинают ходить в туннели по домену.
 */
async function toggleProxy() {
  if (!App) return;
  let applied = false;
  try {
    applied = await App.SystemProxyApplied();
    if (applied) {
      await App.ClearSystemProxy();
      setFooter('Системный прокси снят — прямое соединение');
    } else {
      await App.ApplySystemProxy();
      setFooter('Трафик браузеров направлен в туннели');
    }
  } catch (e) {
    setFooter('Не удалось изменить системный прокси: ' + e);
    logLocal('err', String(e));
  }
  await updateProxyBtn(true);
}

/** Копирование адреса PAC в буфер обмена. */
async function copyPAC() {
  const url = document.getElementById('pacUrl').textContent;
  if (!url) return;
  try {
    await navigator.clipboard.writeText(url);
    setFooter('Адрес PAC скопирован — вставьте его в настройки прокси браузера');
  } catch (e) {
    setFooter('Скопируйте адрес вручную: ' + url);
  }
}

/**
 * Кнопка «↻ с сервера»: запрашивает список групп у VPN-сервера и подставляет
 * его в подсказку поля. Имя группы должно совпадать с алиасом на сервере
 * буквально, а набор групп меняется на стороне шлюза — поэтому список
 * берётся у сервера, а не хранится в приложении.
 */
async function fetchGroups() {
  if (!App) return;
  const endpoint = document.getElementById('fEndpoint').value.trim();
  if (!endpoint) {
    setFooter('Укажите адрес VPN-сервера');
    return;
  }

  setFooter('Запрашиваю список групп у ' + endpoint + '…');
  try {
    const groups = await App.FetchGroups(endpoint) || [];
    const list = document.getElementById('groupOptions');
    list.textContent = '';
    groups.forEach(g => {
      const opt = document.createElement('option');
      opt.value = g;
      list.appendChild(opt);
    });

    if (!groups.length) {
      setFooter('Сервер не предлагает выбор группы — оставьте поле пустым');
      return;
    }
    // Пустое или неизвестное значение заменяем первым из списка, чтобы
    // подключение не падало на несовпадении имени.
    const field = document.getElementById('fGroup');
    if (!groups.includes(field.value.trim())) field.value = groups[0];
    setFooter('Групп получено: ' + groups.length);
  } catch (e) {
    logLocal('err', String(e));
    setFooter('Не удалось получить список групп');
  }
}

/** Кнопка «Сохранить»: валидация и запись конфига на бэкенде. */
async function saveConfig() {
  if (!App) return false;
  collectForm();
  try {
    await App.SaveConfig({ Mode: fullConfig.Mode, Tunnels: tunnels });
    savedSnapshot = formSnapshot(); // с этого момента форма совпадает с бэкендом
    tunnels.forEach(t => markDisconnected(t.Name)); // SaveConfig останавливает туннели
    setFooter('Конфигурация сохранена');
    return true;
  } catch (e) {
    logLocal('err', String(e));
    setFooter('Ошибка сохранения конфигурации');
    return false;
  }
}

/** Слепок конфигурации для сравнения формы с тем, что знает бэкенд. */
function formSnapshot() {
  return JSON.stringify({ Mode: fullConfig && fullConfig.Mode, Tunnels: tunnels });
}

/**
 * Гарантирует, что бэкенд подключается с тем, что показано в форме.
 *
 * Подключение выполняет бэкенд по сохранённой конфигурации, а правка полей
 * меняла только состояние страницы. Из-за этого поменянная в интерфейсе
 * группа игнорировалась: подключение уходило со старым именем и падало на
 * «группа не найдена на сервере».
 *
 * Сохранение перерегистрирует туннели и рвёт активные подключения, поэтому
 * молча сохранять можно только когда ничего не подключено; иначе честно
 * просим нажать «Сохранить».
 */
async function ensureFormApplied() {
  collectForm();
  if (formSnapshot() === savedSnapshot) return true;

  const busy = Object.values(status).some(s => s.connected || s.connecting);
  if (busy) {
    setFooter('Есть несохранённые изменения. Нажмите «Сохранить» — активные туннели будут отключены');
    return false;
  }
  return await saveConfig();
}

/** Отрисовка чипов маршрутов активного туннеля. */
function renderRoutes() {
  const t = tunnels.find(x => x.Name === activeId);
  const box = document.getElementById('routesList');
  box.textContent = '';
  if (!t) return;

  (t.Routes || []).forEach((cidr, i) => {
    const chip = document.createElement('div');
    chip.className = 'route-chip';
    const text = document.createElement('span');
    text.textContent = cidr;
    const del = document.createElement('span');
    del.className = 'del';
    del.textContent = '✕';
    del.addEventListener('click', () => { t.Routes.splice(i, 1); renderRoutes(); });
    chip.append(text, del);
    box.appendChild(chip);
  });

  const add = document.createElement('div');
  add.className = 'route-add-btn';
  add.textContent = '+ добавить';
  add.addEventListener('click', () => {
    const cidr = prompt('Подсеть (CIDR), например 10.0.0.0/24:');
    if (cidr && cidr.trim()) {
      t.Routes = t.Routes || [];
      t.Routes.push(cidr.trim());
      renderRoutes();
    }
  });
  box.appendChild(add);
}

/** Показ/скрытие пароля. */
function togglePass(el) {
  const inp = document.getElementById('fPass');
  inp.type = inp.type === 'password' ? 'text' : 'password';
  el.textContent = inp.type === 'password' ? '👁' : '🙈';
}

// ---------------------------------------------------------------- статистика

/** Обновление карточки статистики для активного туннеля. */
function renderStats() {
  const st = activeId ? status[activeId] : null;
  const time = document.getElementById('stTime');
  const mode = document.getElementById('stMode');
  if (st && st.connected) {
    const m = String(Math.floor(st.seconds / 60)).padStart(2, '0');
    const s = String(st.seconds % 60).padStart(2, '0');
    time.textContent = `${m}:${s}`;
    mode.textContent = (st.mode || currentMode).toUpperCase();
  } else {
    time.textContent = '00:00';
    mode.textContent = '—';
  }
}

/** Кнопка подключения активного туннеля: подпись и цвет по статусу. */
function updateConnectBtn() {
  const btn = document.getElementById('connectBtn');
  const st = activeId ? status[activeId] : null;
  if (st && (st.connected || st.connecting)) {
    btn.textContent = st.connecting ? '⏳ Подключение…' : '⏹ Отключить';
    btn.className = 'btn-connect on';
  } else {
    btn.textContent = '▶ Подключить';
    btn.className = 'btn-connect off';
  }
}

/** Подвал: количество активных туннелей. */
function updateFooterCounts() {
  const n = Object.values(status).filter(s => s.connected).length;
  setFooter(n ? `Активных туннелей: ${n}` : 'Готово');
}

function setFooter(text) {
  document.getElementById('footerStatus').textContent = text;
}

// ---------------------------------------------------------------- 2FA

/** Событие tunnel:2fa — показать модальный диалог ввода кода. */
function showTwoFA(tunnelId) {
  twofaTunnelId = tunnelId;
  document.getElementById('twofaTunnel').textContent = tunnelId;
  document.getElementById('twofaOverlay').classList.remove('hidden');
  setTwoFAError('');
  const inp = document.getElementById('twofaCode');
  inp.value = '';
  inp.focus();
}

/**
 * Отправка 2FA-кода на бэкенд. Окно закрывается только при успехе: раньше
 * оно закрывалось всегда, и после неверного кода туннель молча оставался
 * ждать ввода, а интерфейс навсегда застревал в состоянии «подключение».
 */
async function submitTwoFA() {
  const input = document.getElementById('twofaCode');
  const code = input.value.trim();
  if (!code || !twofaTunnelId) return;
  try {
    await App.Submit2FA(twofaTunnelId, code);
    hideTwoFA();
  } catch (e) {
    logLocal('err', String(e));
    setTwoFAError('Код не принят. Введите новый код.');
    input.value = '';
    input.focus();
  }
}

/** Сообщение об ошибке в окне ввода 2FA-кода. */
function setTwoFAError(text) {
  const box = document.getElementById('twofaError');
  if (box) box.textContent = text || '';
}

/** Закрытие диалога без отправки (туннель останется ждать код). */
function hideTwoFA() {
  document.getElementById('twofaOverlay').classList.add('hidden');
  twofaTunnelId = null;
}

// Enter в поле кода — отправить, Escape — закрыть.
document.addEventListener('keydown', (e) => {
  if (document.getElementById('twofaOverlay').classList.contains('hidden')) return;
  if (e.key === 'Enter') submitTwoFA();
  if (e.key === 'Escape') hideTwoFA();
});

// ---------------------------------------------------------------- журнал

/** Событие "log" / записи GetLogs(): {time, level, message}. */
function appendLog(entry) {
  const box = document.getElementById('logBox');
  const line = document.createElement('div');
  line.className = 'log-line';

  const ts = document.createElement('span');
  ts.className = 'ts';
  ts.textContent = new Date(entry.time).toLocaleTimeString('ru-RU');

  const lvl = document.createElement('span');
  lvl.className = entry.level;
  lvl.textContent = ` [${(entry.level || 'info').toUpperCase()}] `;

  line.append(ts, lvl, document.createTextNode(entry.message));
  box.appendChild(line);
  while (box.childElementCount > 1000) box.removeChild(box.firstChild);
  box.scrollTop = box.scrollHeight;
}

/** Локальная запись в журнал (ошибки самого фронтенда). */
function logLocal(level, message) {
  appendLog({ time: new Date().toISOString(), level, message });
}
