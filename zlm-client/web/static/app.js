const $ = (id) => document.getElementById(id);
const THEME_KEY = 'zlm-theme';

function cssVar(name, fallback) {
    const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || fallback;
}

function currentTheme() {
    return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark';
}

function applyTheme(theme, reloadCharts) {
    const t = theme === 'light' ? 'light' : 'dark';
    const prev = currentTheme();
    document.documentElement.setAttribute('data-theme', t);
    document.documentElement.style.colorScheme = t;
    try { localStorage.setItem(THEME_KEY, t); } catch (e) { }
    document.querySelectorAll('[data-theme-set]').forEach((b) => {
        b.classList.toggle('is-on', b.getAttribute('data-theme-set') === t);
    });
    if (reloadCharts && prev !== t && pageFromPath() === 'overview') loadCharts();
}

function initThemeToggle() {
    applyTheme(currentTheme());
    if (document.body.dataset.themeBound) return;
    document.body.dataset.themeBound = '1';
    document.body.addEventListener('click', (e) => {
        const btn = e.target.closest && e.target.closest('[data-theme-set]');
        if (!btn) return;
        applyTheme(btn.getAttribute('data-theme-set'), true);
    });
}
let current = null;
let mpegtsPlayer = null;
let hlsPlayer = null;
let rtcPC = null;
let mseAbort = null;
let wsFmp4 = null;
let dashPlayer = null;
let pushPC = null;
let playPC = null;
let pushLocal = null;
let fileHls = null;
let fileMpegts = null;
let recFiles = [];
let logFollow = true;
let chartRange = '1d';
let lastLogText = '';
let logBuf = [];
let logOffset = 0;
let logES = null;
let logRAF = 0;
let logPending = [];
let logFilterTimer = 0;
let playGen = 0;
let playObjectUrl = '';
let filePlayGen = 0;
let fileObjectUrl = '';
let fileMseAbort = null;
const LOG_MAX = 2000;

function rememberVodTarget(app, stream) {
    try {
        if (app) sessionStorage.setItem('zlm-vod-app', app);
        if (stream) sessionStorage.setItem('zlm-vod-stream', stream);
    } catch (e) { }
}

function fillVodCtrlFromMemory() {
    let app = 'vod';
    let stream = '';
    try {
        app = sessionStorage.getItem('zlm-vod-app') || 'vod';
        stream = sessionStorage.getItem('zlm-vod-stream') || '';
    } catch (e) { }
    if ($('vodCtrlApp') && !$('vodCtrlApp').dataset.touched) $('vodCtrlApp').value = app;
    if ($('vodCtrlStream') && !$('vodCtrlStream').dataset.touched && stream) $('vodCtrlStream').value = stream;
}

function openVodDrawer(btn) {
    const d = $('vodDrawer');
    if (!d || !btn) return;
    const path = btn.dataset.path || '';
    const name = btn.dataset.name || path.split('/').pop() || '';
    const stream = String(btn.dataset.stream || name).replace(/\.mp4$/i, '') || 'vod';
    const app = btn.dataset.app || 'vod';
    if ($('vodFilePath')) $('vodFilePath').value = path;
    if ($('vodApp')) $('vodApp').value = app;
    if ($('vodStream')) $('vodStream').value = stream;
    if ($('vodSeek')) $('vodSeek').value = '0';
    if ($('vodSpeed')) $('vodSpeed').value = '1';
    if ($('vodRepeat')) $('vodRepeat').value = '0';
    if ($('vodDrawerFile')) $('vodDrawerFile').textContent = path || name;
    rememberVodTarget(app, stream);
    d.classList.add('is-open');
    d.setAttribute('aria-hidden', 'false');
    const first = $('vodApp');
    if (first) first.focus();
}

function closeVodDrawer() {
    const d = $('vodDrawer');
    if (!d) return;
    d.classList.remove('is-open');
    d.setAttribute('aria-hidden', 'true');
}

function pageFromPath() {
    const p = location.pathname.replace(/\/+$/, '') || '/';
    if (p === '/' || p === '/ui/overview') return 'overview';
    const name = p.replace(/^\//, '');
    if (name === 'rtp' || name === 'onvif-webrtc') return 'protocols';
    return name;
}

function nodeId() {
    return document.body.dataset.node || 'zlm-1';
}

function httpsAdminUrl() {
    const port = document.body.dataset.https || 7789;
    return 'https://' + location.hostname + ':' + port + '/';
}

// 页面上的 8090 公网地址就是给 HLS.js / VLC 用的。
// HTTPS 运维台不能混拉 HTTP 8090，才走同域反代。
// 预览必须用页面上展示的播放地址，才能暴露用户侧同样的跨域 / 编码 / 拦截问题。
// 仅在 HTTPS 页面拉 HTTP 媒体时给出明确失败（浏览器会拦截，用户在 HTTPS 站点同样播不了）。
function assertPublicPlayUrl(url) {
    const s = String(url || '');
    if (!s) {
        setPlayStatus('没有可预览的播放地址', 'error');
        return false;
    }
    if (location.protocol === 'https:' && /^http:\/\//i.test(s)) {
        setPlayStatus('HTTPS 页面无法拉 HTTP 地址（用户在 HTTPS 站点同样会被拦截）: ' + s, 'error');
        return false;
    }
    if (location.protocol === 'https:' && /^ws:\/\//i.test(s)) {
        setPlayStatus('HTTPS 页面无法拉 WS 地址（用户同样会被拦截）: ' + s, 'error');
        return false;
    }
    return true;
}

function webrtcSignalUrl(playUrl, link) {
    const extras = (link && (link.extra || link.Extra)) || [];
    for (let i = 0; i < extras.length; i++) {
        const lab = extras[i].label || extras[i].Label || '';
        const u = extras[i].url || extras[i].URL || '';
        if (u && /HTTP\s*信令/i.test(lab)) return u;
    }
    const m = String(playUrl || '').match(/^webrtc:\/\/([^/:]+)(?::(\d+))?\/(.+)$/i);
    if (!m) return '';
    const host = m[1];
    const port = m[2] || '';
    const parts = m[3].replace(/\/+$/, '').split('/').filter(Boolean);
    if (parts.length < 2) return '';
    const app = parts[parts.length - 2];
    const stream = parts[parts.length - 1];
    const origin = port ? ('http://' + host + ':' + port) : ('http://' + host);
    return origin + '/index/api/webrtc?app=' + encodeURIComponent(app) + '&stream=' + encodeURIComponent(stream) + '&type=play';
}

function zlmHttpPort() {
    return (document.body && document.body.dataset.httpPort) || '8090';
}

function displayedMediaUrl(url) {
    const s = String(url || '').trim();
    if (!s) return '';
    if (/^(https?|wss?):\/\//i.test(s) || /^webrtc:\/\//i.test(s)) return s;
    const zlm = s.match(/\/api\/node\/[^/]+\/zlm\/(.+)$/i);
    if (zlm) return 'http://' + location.hostname + ':' + zlmHttpPort() + '/' + zlm[1];
    const live = s.replace(/\\/g, '/').match(/\/((?:live|vod)\/[^/?#]+\/hls(?:\.fmp4)?\.m3u8)(\?.*)?$/i);
    if (live) return 'http://' + location.hostname + ':' + zlmHttpPort() + '/' + live[1] + (live[2] || '');
    return s;
}

function newRtcPeer() {
    return new RTCPeerConnection({
        iceServers: [],
        bundlePolicy: 'max-bundle',
        rtcpMuxPolicy: 'require',
        iceCandidatePoolSize: 0
    });
}

function dropRelayIceSdp(sdp) {
    const lines = String(sdp || '').split(/\r?\n/);
    const kept = lines.filter((l) => !/^a=candidate:/i.test(l) || !/ typ relay /i.test(l));
    if (kept.length === lines.length) return sdp;
    return kept.join('\r\n');
}

function waitHostIce(pc, ms) {
    return new Promise((resolve) => {
        let done = false;
        const finish = () => { if (!done) { done = true; resolve(); } };
        if (!pc || pc.iceGatheringState === 'complete') { finish(); return; }
        pc.onicecandidate = (ev) => {
            const c = ev && ev.candidate && ev.candidate.candidate;
            if (c && / typ host /.test(c)) finish();
            if (!ev || !ev.candidate) finish();
        };
        pc.onicegatheringstatechange = () => { if (pc.iceGatheringState === 'complete') finish(); };
        setTimeout(finish, ms || 80);
    });
}

function applyRtcLowDelay(pc, videoEl) {
    const hint = (recv) => {
        if (!recv) return;
        try { if ('jitterBufferTarget' in recv) recv.jitterBufferTarget = 0; } catch (e) { }
        try { if ('playoutDelayHint' in recv) recv.playoutDelayHint = 0; } catch (e) { }
    };
    (pc.getReceivers ? pc.getReceivers() : []).forEach(hint);
    if (videoEl) {
        try { videoEl.playsInline = true; } catch (e) { }
    }
}

function syncNav() {
    const page = pageFromPath();
    document.body.dataset.page = page;
    const content = $('content');
    if (content) content.className = 'page-' + page;
    document.querySelectorAll('header nav a').forEach((a) => {
        const href = a.getAttribute('hx-get') || a.getAttribute('href') || '';
        const active = (page === 'overview' && (href === '/' || href === '/ui/overview'))
            || href === '/' + page || href === '/' + page + '/'
            || (href === '/events' && (page === 'events' || page === 'logs'));
        a.classList.toggle('active', active);
    });
}

function showToast(msg) {
    const el = $('toast');
    if (!el) return;
    el.textContent = msg;
    el.classList.add('show');
    clearTimeout(showToast._t);
    showToast._t = setTimeout(() => el.classList.remove('show'), 1600);
}

function toastFromEvent(e) {
    const d = e.detail;
    const v = d && (d.value !== undefined ? d.value : d);
    if (v == null) return;
    showToast(typeof v === 'string' ? v : (v.toast || JSON.stringify(v)));
}

function copyText(text, btn) {
    text = String(text || '');
    if (!text) {
        showToast('没有可复制的地址');
        return Promise.resolve(false);
    }
    const ok = () => {
        if (btn) {
            const old = btn.textContent;
            btn.textContent = '已复制';
            btn.classList.add('copied');
            setTimeout(() => { btn.textContent = old; btn.classList.remove('copied'); }, 1200);
        }
        showToast('已复制到剪贴板');
        return true;
    };
    const fail = () => {
        showToast('复制失败，请手动选中地址 Ctrl+C');
        return false;
    };
    const fallback = () => {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.setAttribute('readonly', '');
        ta.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0';
        document.body.appendChild(ta);
        ta.select();
        let done = false;
        try { done = document.execCommand('copy'); } catch (e) { done = false; }
        document.body.removeChild(ta);
        return done ? ok() : fail();
    };
    if (navigator.clipboard && window.isSecureContext) {
        return navigator.clipboard.writeText(text).then(ok).catch(() => fallback());
    }
    return Promise.resolve(fallback());
}

function escHtml(s) {
    return String(s || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
function fmtBits(bytesPerSec) {
    const bits = Number(bytesPerSec || 0) * 8;
    if (bits < 1000) return Math.round(bits) + ' bps';
    if (bits < 1e6) return (bits / 1e3).toFixed(bits >= 10000 ? 0 : 1) + ' kbps';
    return (bits / 1e6).toFixed(bits >= 1e7 ? 1 : 2) + ' Mbps';
}

function bitAxis(values) {
    let max = 0;
    (values || []).forEach((v) => { if (v > max) max = v; });
    if (max >= 1e6) {
        return { name: 'Mbps', yfmt: (v) => (v / 1e6).toFixed(max >= 1e7 ? 0 : 1) };
    }
    return { name: 'kbps', yfmt: (v) => String(Math.round(v / 1e3)) };
}
function fmtBytes(n) {
    n = Number(n || 0);
    if (n < 1024) return n + ' B';
    if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
    if (n < 1073741824) return (n / 1048576).toFixed(1) + ' MB';
    return (n / 1073741824).toFixed(2) + ' GB';
}
function fmtDuration(sec) {
    sec = Math.max(0, Math.floor(Number(sec || 0)));
    const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
    if (h) return h + 'h ' + m + 'm ' + s + 's';
    if (m) return m + 'm ' + s + 's';
    return s + 's';
}
function fmtGop(s) {
    const ms = Number(s && s.gop_interval_ms);
    if (!ms) return '-';
    const sec = ms / 1000;
    if (sec < 1) return sec.toFixed(1) + 's';
    return Math.round(sec) + 's';
}

function fileUrl(path, dl) {
    return `/api/node/${encodeURIComponent(nodeId())}/file?path=${encodeURIComponent(path)}${dl ? '&dl=1' : ''}`;
}
function mediaUrl(path) {
    return fileUrl(path, false);
}
function fileAbsUrl(path, dl) {
    return location.origin + fileUrl(path, dl);
}

function loadRecFiles() {
    const el = $('recFilesJson');
    if (!el) { recFiles = []; return; }
    try { recFiles = JSON.parse(el.textContent || '[]') || []; }
    catch (e) { recFiles = []; }
}

function syncRecModeUI() {
    const wrap = $('recSegWrap');
    const mode = $('recMode');
    const kind = $('recType');
    const hls = kind && kind.value === 'hls';
    if (mode) mode.style.display = hls ? 'none' : '';
    if (!wrap) return;
    wrap.style.display = (hls || (mode && mode.value === 'single')) ? 'none' : 'flex';
}

function fillCfgPaths() {
    const hls = document.querySelector('#cfgForm input[name="protocol.hls_save_path"]');
    const mp4 = document.querySelector('#cfgForm input[name="protocol.mp4_save_path"]');
    if ($('curHlsPath') && hls && !$('curHlsPath').value) $('curHlsPath').value = hls.value;
    if ($('curMp4Path') && mp4 && !$('curMp4Path').value) $('curMp4Path').value = mp4.value;
    const mp4v = $('curMp4Path') && $('curMp4Path').value;
    if ($('mediaPathBase') && mp4v && /[\\/]mp4$/.test(mp4v) && !$('mediaPathBase').dataset.touched) {
        $('mediaPathBase').value = mp4v.replace(/[\\/]+mp4$/, '');
    }
}

function setCfgFieldErr(inp, msg) {
    const wrap = inp.closest('.cfg-field, .form-row');
    if (!wrap) return false;
    wrap.classList.toggle('cfg-bad', !!msg);
    let err = wrap.querySelector('.cfg-err');
    if (msg) {
        if (!err) {
            err = document.createElement('span');
            err.className = 'cfg-err';
            wrap.appendChild(err);
        }
        err.textContent = msg;
        err.removeAttribute('data-server');
        inp.setCustomValidity(msg);
        return false;
    }
    if (err) err.remove();
    inp.setCustomValidity('');
    return true;
}

function looksAbsPath(p) {
    p = String(p || '').trim();
    if (!p) return false;
    if (p.charAt(0) === '/') return true;
    return /^[A-Za-z]:[\\/]/.test(p);
}

function checkLiveKeepInput(inp, toastOnFail) {
    const rangeText = '有效范围 30-86400 秒';
    const raw = (inp.value || '').trim();
    let msg = '';
    if (!raw) msg = '不能为空，' + rangeText;
    else {
        const n = Number(raw);
        if (!Number.isFinite(n) || n % 1 !== 0 || n < 30 || n > 86400) msg = '数值异常，' + rangeText;
    }
    const ok = setCfgFieldErr(inp, msg);
    if (!ok && toastOnFail) showToast(msg);
    return ok;
}

function checkSnapIntervalInput(inp, toastOnFail) {
    const rangeText = '有效范围 5-300 秒';
    const raw = (inp.value || '').trim();
    let msg = '';
    if (!raw) msg = '不能为空，' + rangeText;
    else {
        const n = Number(raw);
        if (!Number.isFinite(n) || n % 1 !== 0 || n < 5 || n > 300) msg = '数值异常，' + rangeText;
    }
    const ok = setCfgFieldErr(inp, msg);
    if (!ok && toastOnFail) showToast(msg);
    return ok;
}

function checkOpsFieldInput(inp, toastOnFail) {
    if (!inp || !inp.name || inp.type === 'checkbox' || inp.type === 'radio') return true;
    const name = inp.name;
    if (name === 'live_keep_sec') return checkLiveKeepInput(inp, toastOnFail);
    if (name === 'snap_interval') return checkSnapIntervalInput(inp, toastOnFail);
    const v = (inp.value || '').trim();
    let msg = '';
    if (name === 'root' || name === 'base') {
        if (!v) msg = '不能为空';
        else if (!looksAbsPath(v)) msg = '必须是绝对路径';
    } else if (name === 'bin' || name === 'ini' || name === 'log_dir') {
        if (v && !looksAbsPath(v)) msg = '必须是绝对路径';
    } else if (name === 'api') {
        if (v && !/^https?:\/\/[^/\s]+/i.test(v)) msg = '须为 http(s)://host:port';
    } else if (name === 'ffmpeg') {
        const dash = document.querySelector('#opsForm input[name="enable_dash"]');
        if (dash && dash.checked && !v) msg = '开启 DASH 时必须填写 ffmpeg 路径';
        else if (v && !looksAbsPath(v)) msg = '必须是绝对路径';
    }
    const ok = setCfgFieldErr(inp, msg);
    if (!ok && toastOnFail) showToast(msg);
    return ok;
}

function checkZlmCfgInput(inp, toastOnFail) {
    const ph = inp.getAttribute('placeholder') || '';
    const key = String(inp.name || '').toLowerCase();
    const name = key.indexOf('.') >= 0 ? key.slice(key.lastIndexOf('.') + 1) : key;
    const v = (inp.value || '').trim();
    let msg = '';
    const isInt = (s) => /^-?\d+$/.test(s);
    const isUint = (s) => /^\d+$/.test(s);
    const boolNames = {
        apidebug: 1, add_mute_audio: 1, auto_close: 1, dirmenu: 1, allow_cross_domains: 1,
        faststart: 1, fastregister: 1, enablefmp4: 1, segkeep: 1, directproxy: 1, lowlatency: 1
    };
    const boolish = name.indexOf('enable') === 0 || name.endsWith('_demand') || name.endsWith('demand') ||
        key.indexOf('.enable') >= 0 || !!boolNames[name];
    const portish = name === 'port' || name === 'sslport' || name === 'sockport' || name === 'tcpport' ||
        key.endsWith('.port') || key.indexOf('listen_port') >= 0;
    if (name === 'level' && key.indexOf('log.') === 0) {
        if (!isInt(v) || Number(v) < 0 || Number(v) > 4) msg = '日志等级须为 0-4（0=Trace 1=Debug 2=Info 3=Warn 4=Error）';
    } else if (name === 'modify_stamp') {
        if (!isInt(v) || Number(v) < 0 || Number(v) > 2) msg = '须为 0 / 1 / 2';
    } else if (boolish) {
        if (v !== '0' && v !== '1' && v.toLowerCase() !== 'true' && v.toLowerCase() !== 'false') msg = '须为 0 或 1';
    } else if (portish) {
        if (!isUint(v) || Number(v) > 65535) msg = '端口须为 0-65535 的整数';
    } else if (key.indexOf('hook.on_') === 0) {
        if (v && !/^https?:\/\//i.test(v)) msg = 'Hook 地址须为 http:// 或 https://，或留空关闭';
    } else if (ph.indexOf('0 或 1') >= 0 && v && v !== '0' && v !== '1' && v.toLowerCase() !== 'true' && v.toLowerCase() !== 'false') {
        msg = '数值异常，有效范围 0 或 1';
    } else if (ph.indexOf('0-65535') >= 0 && v) {
        const n = Number(v);
        if (!Number.isInteger(n) || n < 0 || n > 65535) msg = '数值异常，有效范围 0-65535';
    } else if (ph.indexOf('0-4') >= 0 && v) {
        const n = Number(v);
        if (!Number.isInteger(n) || n < 0 || n > 4) msg = '数值异常，有效范围 0-4';
    } else if (ph.indexOf('0 / 1 / 2') >= 0 && v) {
        const n = Number(v);
        if (!Number.isInteger(n) || n < 0 || n > 2) msg = '数值异常，有效范围 0 / 1 / 2';
    } else if (ph.indexOf('非负整数') >= 0 || ph.indexOf('0-86400') >= 0) {
        if (!v) msg = '不能为空';
        else if (!isUint(v)) msg = '数值异常，须为非负整数';
        else if (ph.indexOf('0-86400') >= 0 && Number(v) > 86400) msg = '有效范围 0-86400 秒';
    }
    const ok = setCfgFieldErr(inp, msg);
    if (!ok && toastOnFail) showToast(msg);
    return ok;
}

function cfgControlValue(el) {
    if (el.type === 'checkbox' || el.type === 'radio') return el.checked ? (el.value || '1') : '';
    return el.value || '';
}

function snapshotNamedControls(root) {
    const data = {};
    root.querySelectorAll('input, select, textarea').forEach((el) => {
        if (!el.name || el.disabled || el.readOnly) return;
        if (el.type === 'hidden' && String(el.name).indexOf('orig.') === 0) return;
        data[el.name] = cfgControlValue(el);
    });
    return JSON.stringify(data);
}

function isOpsDirty() {
    const form = $('opsForm');
    if (!form) return false;
    return snapshotNamedControls(form) !== (form.dataset.snap || '');
}

function isZlmDirty() {
    const orig = {};
    document.querySelectorAll('input[form="cfgForm"][name^="orig."], #cfgForm input[name^="orig."]').forEach((el) => {
        orig[el.name.slice(5)] = el.value;
    });
    let dirty = false;
    document.querySelectorAll('.cfg-zone-zlm input[form="cfgForm"]:not([type=hidden])').forEach((el) => {
        if ((el.value || '') !== (orig[el.name] != null ? orig[el.name] : '')) dirty = true;
    });
    return dirty;
}

function syncCfgSaveButtons() {
    const opsBtn = $('opsSaveBtn');
    const zlmBtn = $('zlmSaveBtn');
    if (opsBtn) {
        const form = $('opsForm');
        opsBtn.disabled = !isOpsDirty() || !!(form && form.querySelector('.cfg-bad'));
    }
    if (zlmBtn) {
        zlmBtn.disabled = !isZlmDirty() || !!document.querySelector('.cfg-zone-zlm .cfg-bad');
    }
}

function initCfgDirtyGuards() {
    const form = $('opsForm');
    if (form && form.dataset.snap == null) form.dataset.snap = snapshotNamedControls(form);
    syncCfgSaveButtons();
}

function initCfgValueGuards() {
    const page = document.querySelector('.cfg-page');
    if (!page) return;
    const ops = $('opsForm');
    if (ops) {
        ops.querySelectorAll('input, select, textarea').forEach((inp) => {
            if (!inp.name || inp.disabled || inp.readOnly || inp.dataset.guard) return;
            inp.dataset.guard = '1';
            const run = (toastOnFail) => {
                checkOpsFieldInput(inp, toastOnFail);
                if (inp.name === 'enable_dash') {
                    const ff = ops.querySelector('input[name="ffmpeg"]');
                    if (ff) checkOpsFieldInput(ff, toastOnFail);
                }
                syncCfgSaveButtons();
            };
            inp.addEventListener('input', () => run(false));
            inp.addEventListener('change', () => run(false));
            inp.addEventListener('blur', () => run(true));
        });
        if (!ops.dataset.guard) {
            ops.dataset.guard = '1';
            ops.addEventListener('submit', (e) => {
                let ok = true;
                ops.querySelectorAll('input[name], select[name], textarea[name]').forEach((inp) => {
                    if (inp.disabled || inp.readOnly) return;
                    if (!checkOpsFieldInput(inp, false)) ok = false;
                });
                syncCfgSaveButtons();
                if (!ok || ($('opsSaveBtn') && $('opsSaveBtn').disabled)) {
                    e.preventDefault();
                    showToast('请先修正标红参数，或确认已修改运维台配置');
                }
            });
        }
    }
    page.querySelectorAll('.cfg-zone-zlm input:not([type=hidden])').forEach((inp) => {
        if (inp.dataset.guard) return;
        inp.dataset.guard = '1';
        inp.addEventListener('input', () => { checkZlmCfgInput(inp, false); syncCfgSaveButtons(); });
        inp.addEventListener('blur', () => { checkZlmCfgInput(inp, true); syncCfgSaveButtons(); });
    });
    initCfgDirtyGuards();
}

function initCfgSearch() {
    const input = $('cfgSearch');
    if (!input) return;
    if (!input.dataset.bound) {
        input.dataset.bound = '1';
        input.addEventListener('input', applyCfgSearch);
    }
    applyCfgSearch();
}

function applyCfgSearch() {
    const q = (($('cfgSearch') && $('cfgSearch').value) || '').trim().toLowerCase();
    const page = document.querySelector('.cfg-page');
    if (!page) return;
    let shown = 0, total = 0;
    page.querySelectorAll('.cfg-zone-ops .cfg-field').forEach((el) => {
        const text = (el.textContent + ' ' + ((el.querySelector('input') && el.querySelector('input').value) || '')).toLowerCase();
        const ok = !q || text.indexOf(q) >= 0;
        el.classList.toggle('cfg-search-hide', !ok);
    });
    page.querySelectorAll('.cfg-group').forEach((g) => {
        const sec = ((g.querySelector('.cfg-fold') && g.querySelector('.cfg-fold').textContent) || '').toLowerCase();
        const secHit = q && sec.indexOf(q) >= 0;
        let hit = 0, rows = 0;
        g.querySelectorAll('.form-row').forEach((row) => {
            rows++;
            const lab = (row.querySelector('label') && row.querySelector('label').textContent) || '';
            const key = (row.querySelector('label') && row.querySelector('label').title) || '';
            const inp = row.querySelector('input:not([type=hidden])');
            const val = (inp && inp.value) || '';
            const text = (lab + ' ' + key + ' ' + (inp && inp.name || '') + ' ' + val).toLowerCase();
            const ok = !q || secHit || text.indexOf(q) >= 0;
            row.classList.toggle('cfg-search-hide', !ok);
            if (ok) { hit++; shown++; }
        });
        total += rows;
        const show = !q || secHit || hit > 0;
        g.classList.toggle('cfg-search-hide', !show);
        if (q && show) g.classList.remove('collapsed');
    });
    page.querySelectorAll('.cfg-cat').forEach((cat) => {
        const any = cat.querySelector('.cfg-group:not(.cfg-search-hide)');
        cat.classList.toggle('cfg-search-hide', !!q && !any);
        if (q && any) cat.classList.remove('collapsed');
    });
    const hint = $('cfgSearchHint');
    if (hint) hint.textContent = q ? ('匹配 ' + shown + ' / ' + total + ' 项') : '';
}

function initStreamTableScroll() {
    const frame = document.querySelector('.stream-table-frame');
    if (!frame) return;
    if (frame._streamTableScrollRefresh) {
        frame._streamTableScrollRefresh();
        return;
    }
    const wrap = frame.querySelector('.table-wrap');
    const table = frame.querySelector('.stream-table');
    const scroller = frame.querySelector('.stream-x-scroll');
    const space = frame.querySelector('.stream-x-scroll-space');
    if (!wrap || !table || !scroller || !space) return;

    let raf = 0;
    const refresh = () => {
        cancelAnimationFrame(raf);
        raf = requestAnimationFrame(() => {
            const nameCol = table.querySelector('.col-name');
            const pullCol = table.querySelector('.col-pull');
            const clientCol = table.querySelector('.col-clients');
            const actCol = table.querySelector('.col-act');
            const left = Math.round((nameCol && nameCol.getBoundingClientRect().width) || 300);
            const pull = Math.round((pullCol && pullCol.getBoundingClientRect().width) || 160);
            const clients = Math.round((clientCol && clientCol.getBoundingClientRect().width) || 88);
            const act = Math.round((actCol && actCol.getBoundingClientRect().width) || 108);
            const right = pull + clients + act;
            frame.style.setProperty('--stream-fixed-left', left + 'px');
            frame.style.setProperty('--stream-fixed-right', right + 'px');
            frame.style.setProperty('--stream-fixed-pull', pull + 'px');
            frame.style.setProperty('--stream-fixed-clients', clients + 'px');
            frame.style.setProperty('--stream-fixed-act', act + 'px');
            frame.style.setProperty('--stream-viewport', Math.round(wrap.clientWidth) + 'px');
            const fixedWidth = left + right;
            const viewportWidth = Math.max(0, wrap.clientWidth - fixedWidth);
            const contentWidth = Math.max(viewportWidth, table.scrollWidth - fixedWidth);
            space.style.width = Math.ceil(contentWidth) + 'px';
            scroller.hidden = table.scrollWidth <= wrap.clientWidth + 1;
            if (!scroller.hidden && scroller.scrollLeft !== wrap.scrollLeft) {
                scroller.scrollLeft = wrap.scrollLeft;
            }
        });
    };

    scroller.addEventListener('scroll', () => {
        if (wrap.scrollLeft !== scroller.scrollLeft) wrap.scrollLeft = scroller.scrollLeft;
    }, { passive: true });
    wrap.addEventListener('scroll', () => {
        if (scroller.scrollLeft !== wrap.scrollLeft) scroller.scrollLeft = wrap.scrollLeft;
    }, { passive: true });
    wrap.addEventListener('wheel', (e) => {
        const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : (e.shiftKey ? e.deltaY : 0);
        if (!delta || scroller.hidden) return;
        scroller.scrollLeft += delta;
        e.preventDefault();
    }, { passive: false });

    frame._streamTableScrollRefresh = refresh;
    if (window.ResizeObserver) {
        frame._streamTableScrollObserver = new ResizeObserver(refresh);
        frame._streamTableScrollObserver.observe(frame);
        frame._streamTableScrollObserver.observe(table);
    } else {
        window.addEventListener('resize', refresh);
    }
    refresh();
}

function initStreamSplit() {
    const split = document.getElementById('streamSplit') || document.querySelector('.stream-split');
    if (!split || split._streamSplitBound) return;
    const handle = split.querySelector('.stream-split-handle');
    if (!handle) return;
    split._streamSplitBound = true;
    const key = 'zlm-stream-split-ratio-v2';
    const minLeft = Math.max(240, Number(split.dataset.leftMin) || 360);
    const applyRight = (rightPx, persist) => {
        const total = Math.max(1, split.clientWidth);
        const handleW = handle.offsetWidth || 8;
        const maxRight = Math.max(0, total - minLeft - handleW);
        let right = Math.max(0, Math.min(maxRight, Number(rightPx) || 0));
        if (right < 28) {
            split.classList.add('is-closed');
            split.style.gridTemplateColumns = '';
            return;
        }
        split.classList.remove('is-closed');
        split.style.gridTemplateColumns = 'minmax(' + minLeft + 'px, 1fr) ' + handleW + 'px ' + Math.round(right) + 'px';
        if (persist !== false) {
            try { localStorage.setItem(key, String(right / total)); } catch (e) { }
        }
    };
    const openDefault = () => {
        const total = Math.max(1, split.clientWidth);
        const saved = Number(localStorage.getItem(key));
        const ratio = (saved > 0.12 && saved < 0.85) ? saved : (1 / 2);
        applyRight(total * ratio, false);
    };
    if (!split.classList.contains('is-closed')) openDefault();
    let dragging = false;
    const onMove = (e) => {
        if (!dragging) return;
        const rect = split.getBoundingClientRect();
        applyRight(rect.right - e.clientX, true);
        const frame = split.querySelector('.stream-table-frame');
        if (frame && typeof frame._streamTableScrollRefresh === 'function') frame._streamTableScrollRefresh();
    };
    const stop = () => {
        if (!dragging) return;
        dragging = false;
        split.classList.remove('is-dragging');
        window.removeEventListener('pointermove', onMove);
        window.removeEventListener('pointerup', stop);
    };
    handle.addEventListener('pointerdown', (e) => {
        if (e.button !== 0) return;
        e.preventDefault();
        dragging = true;
        split.classList.add('is-dragging');
        window.addEventListener('pointermove', onMove);
        window.addEventListener('pointerup', stop);
        try { handle.setPointerCapture(e.pointerId); } catch (err) { }
    });
    handle.addEventListener('pointercancel', stop);
}

function selectedRowChecks(root) {
    const table = document.querySelector('[data-batch-root="' + root + '"]');
    if (!table) return [];
    return Array.prototype.slice.call(table.querySelectorAll('.row-check:checked'));
}

function syncBatchBar(root) {
    const table = document.querySelector('[data-batch-root="' + root + '"]');
    const bar = document.querySelector('[data-batch-for="' + root + '"]');
    if (!bar) return;
    const boxes = table ? table.querySelectorAll('.row-check') : [];
    const sel = table ? table.querySelectorAll('.row-check:checked') : [];
    const n = sel.length;
    bar.querySelectorAll('[data-batch-count]').forEach((el) => { el.textContent = String(n); });
    bar.querySelectorAll('[data-batch-submit], [data-batch-download]').forEach((el) => { el.disabled = n === 0; });
    const all = document.querySelector('#batchCheckAll[data-batch-root="' + root + '"]');
    if (all) {
        all.checked = boxes.length > 0 && n === boxes.length;
        all.indeterminate = n > 0 && n < boxes.length;
    }
}

function initBatchSelect() {
    syncBatchBar('sessions');
    syncBatchBar('files');
}

function bindBatchSelectOnce() {
    if (document.body.dataset.batchBound) return;
    document.body.dataset.batchBound = '1';
    document.body.addEventListener('change', (e) => {
        const t = e.target;
        if (!t) return;
        if (t.id === 'batchCheckAll') {
            const root = t.getAttribute('data-batch-root');
            const table = document.querySelector('[data-batch-root="' + root + '"]');
            if (table) table.querySelectorAll('.row-check').forEach((el) => { el.checked = !!t.checked; });
            syncBatchBar(root);
            return;
        }
        if (t.classList && t.classList.contains('row-check')) {
            const table = t.closest('[data-batch-root]');
            if (table) syncBatchBar(table.getAttribute('data-batch-root'));
        }
    });
    document.body.addEventListener('click', (e) => {
        const btn = e.target.closest('[data-batch-download]');
        if (!btn || btn.disabled) return;
        e.preventDefault();
        selectedRowChecks('files').forEach((el, i) => {
            const href = el.getAttribute('data-dl');
            if (!href) return;
            window.setTimeout(() => {
                const a = document.createElement('a');
                a.href = href;
                a.setAttribute('download', el.getAttribute('data-name') || '');
                document.body.appendChild(a);
                a.click();
                a.remove();
            }, i * 180);
        });
    });
}

bindBatchSelectOnce();


function recColSpec(th, title, isCheck, isAct) {
    const cls = th.className || '';
    if (cls.indexOf('col-idx') >= 0 || title === '序号') {
        return { width: 56, minWidth: 48, hozAlign: 'center', headerSort: false, frozen: true };
    }
    if (isCheck) return { width: 44, minWidth: 44, hozAlign: 'center', headerSort: false, frozen: true };
    if (cls.indexOf('col-file') >= 0 || title === '文件') return { minWidth: 160, widthGrow: 1.4 };
    if (cls.indexOf('col-dir') >= 0 || title === '目录') return { minWidth: 140, widthGrow: 2 };
    if (cls.indexOf('col-type') >= 0 || title === '类型') return { width: 110, minWidth: 90 };
    if (cls.indexOf('col-size') >= 0 || title === '大小') return { width: 88, minWidth: 72 };
    if (cls.indexOf('col-dur') >= 0 || title === '时长') return { width: 80, minWidth: 64 };
    if (cls.indexOf('col-mtime') >= 0 || title === '修改时间') return { width: 158, minWidth: 140 };
    if (isAct) return { minWidth: 300, widthGrow: 0, frozen: true };
    return {};
}

function opsColSpec(th, title, isCheck, isAct) {
    if (isCheck) return { width: 44, minWidth: 44, headerSort: false, frozen: true, widthGrow: 0 };
    if (title === '逐项操作') return { width: 220, minWidth: 180, widthGrow: 0, headerSort: false };
    if (isAct || (th.classList && th.classList.contains('col-act'))) {
        return { width: 88, minWidth: 80, maxWidth: 100, widthGrow: 0, headerSort: false, hozAlign: 'center', frozen: true };
    }
    if (/URL/.test(title) || title === '命令') return { minWidth: 180, widthGrow: 2 };
    if (title === '状态' || title === '详情') return { width: 96, minWidth: 80, widthGrow: 0 };
    return { minWidth: 88, widthGrow: 1 };
}

function initOpsTables() {
    if (typeof Tabulator === 'undefined') return;
    document.querySelectorAll('table.js-grid').forEach((table) => {
        if (table.dataset.gridReady === '1') return;
        const headers = Array.from((table.tHead && table.tHead.rows[0] && table.tHead.rows[0].cells) || []);
        const rows = Array.from((table.tBodies[0] && table.tBodies[0].rows) || []);
        if (!headers.length || !rows.length) return;
        if (rows.some((tr) => Array.from(tr.cells).some((td) => td.colSpan > 1))) return;
        const isRec = table.classList.contains('rec-table');
        const columns = headers.map((th, i) => {
            const title = th.textContent.trim() || ' ';
            const htmlTitle = th.innerHTML;
            const isCheck = th.classList.contains('col-check') || (i === 0 && th.querySelector('input[type="checkbox"]'));
            const isAct = th.classList.contains('col-rec-act') || th.classList.contains('col-act') || /操作/.test(title);
            const spec = isRec ? recColSpec(th, title, isCheck, isAct) : opsColSpec(th, title, isCheck, isAct);
            return {
                title: isCheck ? htmlTitle : title,
                titleFormatter: isCheck ? 'html' : 'plaintext',
                field: 'c' + i,
                formatter: 'html',
                headerSort: spec.headerSort != null ? spec.headerSort : (!isCheck && !isAct),
                hozAlign: spec.hozAlign || 'left',
                headerHozAlign: isRec ? 'center' : 'left',
                vertAlign: 'middle',
                frozen: spec.frozen != null ? spec.frozen : !!(isCheck || isAct),
                width: spec.width != null ? spec.width : (isCheck ? 48 : undefined),
                minWidth: spec.minWidth != null ? spec.minWidth : (isAct ? 88 : 72),
                maxWidth: spec.maxWidth,
                widthGrow: spec.widthGrow != null ? spec.widthGrow : (isAct ? 0 : 1)
            };
        });
        const data = rows.map((tr) => {
            const rec = {};
            Array.from(tr.cells).forEach((td, i) => { rec['c' + i] = td.innerHTML; });
            return rec;
        });
        const mount = document.createElement('div');
        mount.className = isRec ? 'ops-grid rec-grid' : 'ops-grid';
        table.replaceWith(mount);
        mount.dataset.gridReady = '1';
        const grid = new Tabulator(mount, {
            data,
            columns,
            layout: 'fitColumns',
            resizableColumns: true,
            movableColumns: false,
            placeholder: '暂无数据',
            reactiveData: false
        });
        grid.on('tableBuilt', () => {
            if (window.htmx) htmx.process(mount);
            initBatchSelect();
        });
    });
}

function onPageEnter() {
    const page = pageFromPath();
    syncNav();
    initThemeToggle();
    if (page !== 'logs' && !(page === 'events' && $('logText'))) stopLogViewer();
    if (page === 'overview') loadCharts();
    if (page === 'logs' || (page === 'events' && $('logText'))) startLogViewer();
    if (page === 'push') preparePushPage();
    if ((page === 'events' || page === 'logs') && $('qEvent')) initEventFilters();
    if (page === 'files') { loadRecFiles(); syncRecModeUI(); fillVodCtrlFromMemory(); }
    if (page === 'config') { fillCfgPaths(); initCfgSearch(); initCfgValueGuards(); }
    if (page === 'streams') { initStreamTableScroll(); initStreamSplit(); }
    initOpsTables();
    initBatchSelect();
}

document.body.addEventListener('toast', toastFromEvent);
document.body.addEventListener('htmx:beforeRequest', (e) => {
    const el = e.detail && e.detail.elt;
    if (!el || el.id !== 'zlmSaveBtn') return;
    syncCfgSaveButtons();
    if (el.disabled || document.querySelector('.cfg-zone-zlm .cfg-bad')) {
        e.preventDefault();
        showToast('请先修正标红参数，或确认已修改 ZLM 配置');
    }
});
document.body.addEventListener('htmx:afterSwap', (e) => {
    const t = e.detail && e.detail.target;
    if (!t) return;
    if (!$('content')) {
        const main = document.querySelector('main');
        if (main) {
            const el = (t.id && main.contains(t)) ? t : main.firstElementChild;
            if (el) {
                el.id = 'content';
                el.setAttribute('hx-history-elt', '');
            }
        }
        onPageEnter();
        return;
    }
    if (t.id === 'content') onPageEnter();
    const frame = t.closest && t.closest('.stream-table-frame');
    if (frame && frame._streamTableScrollRefresh) frame._streamTableScrollRefresh();
});
document.body.addEventListener('htmx:historyRestore', onPageEnter);
document.addEventListener('DOMContentLoaded', onPageEnter);

function zlmPlay(btn) {
    if (!btn || !btn.dataset.play) return;
    const [id, vhost, app, stream] = btn.dataset.play.split('|');
    openPlayer(id, vhost, app, stream, null);
}
window.zlmPlay = zlmPlay;

document.addEventListener('click', (e) => {
    const play = e.target.closest('[data-play]');
    if (play) {
        e.preventDefault();
        e.stopPropagation();
        zlmPlay(play);
        return;
    }
    const filePlay = e.target.closest('.file-play');
    if (filePlay) {
        previewFile(filePlay.dataset.path, filePlay.dataset.kind, filePlay.dataset.playlist, filePlay.dataset.role);
        return;
    }
    const fileCopy = e.target.closest('.file-copy');
    if (fileCopy) {
        copyText(fileCopy.dataset.url || fileAbsUrl(fileCopy.dataset.path, true), fileCopy);
        return;
    }
    const dirJump = e.target.closest('.path-jump');
    if (dirJump) {
        e.preventDefault();
        copyText(dirJump.dataset.path || dirJump.textContent, dirJump);
        return;
    }
    const vodOpen = e.target.closest('.file-vod-open');
    if (vodOpen) {
        e.preventDefault();
        openVodDrawer(vodOpen);
        return;
    }
    if (e.target.closest('[data-close-drawer]')) {
        closeVodDrawer();
        return;
    }
    const appRow = e.target.closest('tr.app-row');
    if (appRow && !e.target.closest('a, button, input, label, form')) {
        e.preventDefault();
        let n = appRow.nextElementSibling;
        const hide = !appRow.classList.contains('is-collapsed');
        appRow.classList.toggle('is-collapsed', hide);
        const label = appRow.querySelector('.col-name, td:not(.col-check)');
        if (label) {
            label.textContent = label.textContent.replace(/^[▾▸\-–]\s*/, hide ? '▸ ' : '▾ ');
        }
        while (n && !n.classList.contains('app-row')) {
            n.classList.toggle('collapsed-app', hide);
            n = n.nextElementSibling;
        }
        return;
    }
    const h4 = e.target.closest('#cfgGroups h4');
    if (h4) h4.parentElement.classList.toggle('collapsed');
    const rangeBtn = e.target.closest('#chartRange button');
    if (rangeBtn) {
        chartRange = rangeBtn.dataset.range;
        document.querySelectorAll('#chartRange button').forEach((x) => x.classList.toggle('active', x === rangeBtn));
        loadCharts();
    }
});

document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    const d = $('vodDrawer');
    if (d && d.classList.contains('is-open')) {
        closeVodDrawer();
        e.preventDefault();
    }
});

document.addEventListener('submit', (e) => {
    const form = e.target;
    if (!(form instanceof HTMLFormElement)) return;
    const submitter = e.submitter;
    const action = ((submitter && (submitter.getAttribute('formaction') || submitter.getAttribute('hx-post'))) ||
        form.getAttribute('hx-post') || form.getAttribute('action') || '').trim();
    const zlmPost = form.hasAttribute('data-zlm-post') || form.id === 'vodLoadForm' ||
        /\/files\/(vod|record)\//.test(action);
    if (!zlmPost || !action) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    if (form.id === 'vodLoadForm') {
        const path = $('vodFilePath') && $('vodFilePath').value.trim();
        if (!path) {
            showToast('未选择 MP4 文件');
            return;
        }
    }
    const ask = (submitter && (submitter.getAttribute('hx-confirm') || submitter.getAttribute('data-confirm'))) ||
        form.getAttribute('hx-confirm');
    if (ask && !window.confirm(ask)) return;
    const fd = new FormData(form);
    const include = form.getAttribute('hx-include');
    if (include) {
        document.querySelectorAll(include).forEach((el) => {
            if (!el.name) return;
            if (el.type === 'checkbox' || el.type === 'radio') {
                if (el.checked) fd.set(el.name, el.value);
                return;
            }
            fd.set(el.name, el.value);
        });
    }
    if (form.getAttribute('data-batch-form') === 'files') {
        const paths = selectedRowChecks('files').map((el) => el.value);
        if (!paths.length) {
            showToast('请先勾选要操作的行');
            return;
        }
        paths.forEach((p) => fd.append('file_path', p));
    }
    const values = {};
    fd.forEach((v, k) => {
        if (values[k] === undefined) { values[k] = v; return; }
        if (!Array.isArray(values[k])) values[k] = [values[k]];
        values[k].push(v);
    });
    if (form.id === 'vodLoadForm' || form.id === 'vodCtrlForm') {
        rememberVodTarget(values.app, values.stream);
    }
    if (window.htmx) {
        htmx.ajax('POST', action, {
            target: '#content',
            swap: 'innerHTML',
            values,
        });
        return;
    }
    const body = new URLSearchParams();
    Object.keys(values).forEach((k) => body.append(k, values[k]));
    fetch(action, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: body.toString(),
        credentials: 'include',
    }).then(() => { location.reload(); }).catch((err) => showToast('操作失败: ' + ((err && err.message) || err)));
}, true);

document.body.addEventListener('htmx:configRequest', (e) => {
    const path = String((e.detail && e.detail.path) || '');
    const elt = e.detail && e.detail.elt;
    const form = elt && elt.closest && elt.closest('form');
    if (/\/files\/(vod|record)\//.test(path)) {
        e.detail.verb = 'post';
        if (form) {
            const fd = new FormData(form);
            const params = e.detail.parameters || {};
            fd.forEach((v, k) => { params[k] = v; });
            e.detail.parameters = params;
        }
    }
    if (form && (form.getAttribute('data-batch-form') === 'sessions' || path.indexOf('kick-selected') >= 0)) {
        const params = e.detail.parameters || {};
        params.id = selectedRowChecks('sessions').map((box) => box.value);
        e.detail.parameters = params;
    }
});

document.addEventListener('change', (e) => {
    if (e.target && (e.target.id === 'recMode' || e.target.id === 'recType')) syncRecModeUI();
    if (e.target && e.target.id === 'mediaPathBase') e.target.dataset.touched = '1';
    if (e.target && e.target.id === 'logFile') { logFollow = true; startLogViewer(true); }
    if (e.target && e.target.classList.contains('logLv')) { logFollow = true; startLogViewer(true); }
    if (e.target && e.target.id === 'eventKind') filterHookEvents();
    if (e.target && e.target.id === 'pushScreen' && e.target.checked && $('pushCam')) $('pushCam').checked = false;
    if (e.target && e.target.id === 'pushCam' && e.target.checked && $('pushScreen')) $('pushScreen').checked = false;
    if (e.target && (e.target.id === 'pushPlayUrl' || e.target.id === 'pushType')) applyPlayUrl(false);
    if (e.target && e.target.id === 'monRoot') {
        const root = e.target.value.trim().replace(/[\\/]+$/, '');
        if (!root) return;
        if ($('monIni') && !$('monIni').value.trim()) $('monIni').value = root + '/config.ini';
        if ($('monLog') && !$('monLog').value.trim()) $('monLog').value = root + '/log';
        if ($('monBin') && !$('monBin').value.trim()) $('monBin').value = root + '/MediaServer';
    }
});

document.addEventListener('input', (e) => {
    if (e.target && (e.target.id === 'vodCtrlApp' || e.target.id === 'vodCtrlStream')) e.target.dataset.touched = '1';
    if (e.target && e.target.id === 'qLog') scheduleFilterLog();
    if (e.target && e.target.id === 'qEvent') filterHookEvents();
    if (e.target && e.target.id === 'pushPlayUrl') applyPlayUrl(true);
});

document.addEventListener('click', (e) => {
    if (e.target && e.target.id === 'btnRefresh') {
        const page = pageFromPath();
        const path = location.pathname + location.search;
        if (window.htmx) htmx.ajax('GET', path === '/' ? '/' : path, { target: '#content', swap: 'innerHTML' });
        if (page === 'overview') loadCharts();
        if (page === 'logs') startLogViewer(true);
    }
    if (e.target && e.target.id === 'closePlay') closePlayer();
    if (e.target && e.target.id === 'closeFile') closeFilePreview();
    if (e.target && e.target.id === 'copyVlc') copyText(current && current.activeUrl, $('copyVlc'));
    if (e.target && e.target.id === 'floatCopy') copyText(($('floatUrl') && $('floatUrl').textContent) || (current && current.activeUrl), $('floatCopy'));
    if (e.target && e.target.id === 'logReload') { logFollow = true; startLogViewer(true); }
    if (e.target && e.target.id === 'logBottom') {
        logFollow = true;
        const box = $('logText');
        if (box) box.scrollTop = box.scrollHeight;
        if ($('logFollowHint')) $('logFollowHint').textContent = '跟随最新';
        if (!logES) startLogStream();
    }
    if (e.target && e.target.id === 'btnPushStart') startPush();
    if (e.target && e.target.id === 'btnPushStop') stopPush();
    if (e.target && e.target.id === 'btnPushDevices') listPushDevices(false).catch((err) => setPushStatus('刷新设备失败: ' + gumError(err)));
});

document.addEventListener('click', (e) => {
    if (e.target && e.target.id === 'overlay') closePlayer();
    if (e.target && e.target.id === 'fileOverlay') closeFilePreview();
});

document.addEventListener('scroll', (e) => {
    if (e.target && e.target.id === 'logText') {
        const box = e.target;
        const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 40;
        logFollow = atBottom;
        if ($('logFollowHint')) $('logFollowHint').textContent = atBottom ? '跟随最新' : '已暂停实时（滚到最新继续）';
        if (atBottom) {
            if (!logES && pageFromPath() === 'logs') startLogStream();
        } else {
            stopLogStream();
        }
    }
}, true);

function logLevels() {
    const checked = [...document.querySelectorAll('.logLv:checked')].map((x) => x.value);
    return checked.length ? checked.join('') : 'DIWE';
}
function logLevel(line) {
    const m = line.match(/(?:^|\s)([DIWE])\s+\[/);
    if (m) return m[1];
    if (/\sE\s/.test(line)) return 'E';
    if (/\sW\s/.test(line)) return 'W';
    if (/\sD\s/.test(line)) return 'D';
    return 'I';
}
function stopLogStream() {
    if (logES) { logES.close(); logES = null; }
    logPending = [];
    if (logRAF) { cancelAnimationFrame(logRAF); logRAF = 0; }
}
function stopLogViewer() { stopLogStream(); }
async function startLogViewer() {
    if (pageFromPath() !== 'logs') return;
    stopLogStream();
    await loadLogSnapshot();
    if (logFollow) startLogStream();
}
async function loadLogSnapshot() {
    const fileEl = $('logFile');
    const q = new URLSearchParams({ node: nodeId(), n: '1200', lv: logLevels() });
    if (fileEl && fileEl.value) q.set('file', fileEl.value);
    let d;
    try {
        d = await (await fetch('/api/logs?' + q)).json();
    } catch (e) {
        if ($('logMeta')) $('logMeta').textContent = '读取失败: ' + e.message;
        if ($('logText')) $('logText').textContent = '';
        logBuf = [];
        return;
    }
    const files = d.files || [];
    const sel = $('logFile');
    if (sel) {
        const sig = files.map((f) => f.name + ':' + f.size).join(',');
        if (sel.dataset.sig !== sig) {
            sel.dataset.sig = sig;
            const cur = sel.value || d.file || '';
            sel.innerHTML = files.map((f) => `<option value="${escHtml(f.name)}">${escHtml(f.name)} (${fmtBytes(f.size)})</option>`).join('') || '<option value="">无日志文件</option>';
            if (cur) sel.value = cur;
        }
    }
    logOffset = Number(d.offset || d.size || 0);
    logBuf = Array.isArray(d.lines) ? d.lines.slice(-LOG_MAX) : [];
    lastLogText = '';
    if ($('logMeta')) $('logMeta').textContent = [d.dir, d.file, (d.size ? fmtBytes(d.size) : ''), d.msg].filter(Boolean).join(' · ') || '本机 ZLM 日志';
    paintLogView(true);
}
function startLogStream() {
    if (pageFromPath() !== 'logs' || logES) return;
    const fileEl = $('logFile');
    const q = new URLSearchParams({ node: nodeId(), offset: String(logOffset || 0), lv: logLevels() });
    if (fileEl && fileEl.value) q.set('file', fileEl.value);
    logES = new EventSource('/api/logs/stream?' + q);
    logES.onmessage = (ev) => {
        let line = '';
        try { line = (JSON.parse(ev.data) || {}).line || ''; } catch (e) { line = ev.data || ''; }
        if (!line) return;
        logPending.push(line);
        if (!logRAF) logRAF = requestAnimationFrame(flushLogPending);
    };
    logES.addEventListener('logerr', (ev) => {
        let msg = '';
        try { msg = JSON.parse(ev.data || '{}').msg || ''; } catch (e) { }
        if (msg && $('logMeta')) $('logMeta').textContent = '实时日志: ' + msg;
    });
    logES.onerror = () => {
        stopLogStream();
        if (pageFromPath() === 'logs' && logFollow) {
            setTimeout(() => { if (pageFromPath() === 'logs' && logFollow && !logES) startLogViewer(true); }, 2000);
        }
    };
}
function flushLogPending() {
    logRAF = 0;
    if (!logPending.length) return;
    const batch = logPending;
    logPending = [];
    logBuf.push.apply(logBuf, batch);
    if (logBuf.length > LOG_MAX) logBuf = logBuf.slice(-LOG_MAX);
    appendLogLines(batch);
}
function logPass(line) {
    const kw = (($('qLog') && $('qLog').value) || '').trim().toLowerCase();
    const allow = new Set([...document.querySelectorAll('.logLv:checked')].map((x) => x.value));
    if (allow.size && !allow.has(logLevel(line))) return false;
    if (kw && !line.toLowerCase().includes(kw)) return false;
    return true;
}
function makeLogSpan(line) {
    const s = document.createElement('span');
    s.className = 'lv-' + logLevel(line);
    s.textContent = line;
    return s;
}
function appendLogLines(lines) {
    const box = $('logText');
    if (!box) return;
    const stick = logFollow || (box.scrollTop + box.clientHeight >= box.scrollHeight - 48);
    const frag = document.createDocumentFragment();
    let added = 0;
    for (let i = 0; i < lines.length; i++) {
        if (!logPass(lines[i])) continue;
        frag.appendChild(makeLogSpan(lines[i]));
        added++;
    }
    if (added) {
        if (!box.firstElementChild) box.textContent = '';
        box.appendChild(frag);
    }
    const extra = box.childNodes.length - LOG_MAX;
    for (let i = 0; i < extra; i++) box.removeChild(box.firstChild);
    if (stick) box.scrollTop = box.scrollHeight;
    if ($('logFollowHint')) $('logFollowHint').textContent = logFollow ? '实时跟随' : '已暂停实时（滚到最新继续）';
}
function scheduleFilterLog() {
    clearTimeout(logFilterTimer);
    logFilterTimer = setTimeout(() => paintLogView(false), 120);
}
function paintLogView(keepStick) {
    const box = $('logText');
    if (!box) return;
    const stick = keepStick || logFollow || (box.scrollTop + box.clientHeight >= box.scrollHeight - 48);
    const lines = logBuf.filter(logPass);
    box.textContent = '';
    const frag = document.createDocumentFragment();
    const view = lines.length > LOG_MAX ? lines.slice(-LOG_MAX) : lines;
    if (!view.length) {
        box.textContent = (($('qLog') && $('qLog').value) || '').trim() ? '没有匹配的日志行' : '暂无日志内容';
    } else {
        for (let i = 0; i < view.length; i++) frag.appendChild(makeLogSpan(view[i]));
        box.appendChild(frag);
    }
    if (stick) box.scrollTop = box.scrollHeight;
}

async function openPlayer(nodeId0, vhost, app, stream, media) {
    closePlayer();
    current = { nodeId: nodeId0, vhost, app, stream, media: media || null, links: [], urls: {}, activeUrl: '' };
    if ($('playTitle')) $('playTitle').textContent = `${app}/${stream}`;
    const d = await (await fetch(`/api/node/${nodeId0}/playurls?host=${encodeURIComponent(location.hostname)}&vhost=${encodeURIComponent(vhost)}&app=${encodeURIComponent(app)}&stream=${encodeURIComponent(stream)}`)).json();
    const links = d.links || Object.entries(d.urls || {}).map(([id, url]) => ({ id, label: id.toUpperCase(), url, web_play: !['rtsp', 'rtmp', 'srt'].includes(id) }));
    current.links = links;
    current.urls = d.urls || Object.fromEntries(links.map((l) => [l.id, l.url]));
    current.enableDash = !!d.enable_dash || (document.body && document.body.dataset.dash === '1');
    renderProtoTabs(links);
    $('overlay').classList.add('show');
    if (!current.media) {
        try {
            const det = await (await fetch(`/api/node/${nodeId0}/detail`)).json();
            current.media = (det.streams || []).find((s) => s.app === app && s.stream === stream) || null;
        } catch (e) { }
    }
    renderMediaStats(current.media);
    playProto('http-flv');
}

function findLink(id) {
    return (current && current.links || []).find((l) => l.id === id) || { id, label: id, url: current && current.urls && current.urls[id], web_play: false };
}

function renderProtoTabs(links) {
    const nativeId = { rtsp: 1, rtmp: 1, srt: 1, gb28181: 1, dash: 1 };
    const web = links.filter((l) => !nativeId[l.id]);
    const native = links.filter((l) => nativeId[l.id]);
    const btn = (l) => `<button class="ghost${nativeId[l.id] ? ' is-native' : ''}" type="button" data-proto="${escHtml(l.id)}">${escHtml(l.label)}</button>`;
    let html = web.map(btn).join('');
    if (native.length) html += '<span class="tab-split" title="浏览器不支持预览">|</span>' + native.map(btn).join('');
    $('protoTabs').innerHTML = html;
    $('protoTabs').querySelectorAll('button').forEach((b) => { b.onclick = () => playProto(b.dataset.proto); });
}

function renderLinkPanel(link) {
    const url = link.url || '';
    if ($('floatProto')) $('floatProto').textContent = link.label || link.id || '协议';
    if ($('floatUrl')) { $('floatUrl').textContent = url; $('floatUrl').title = url; }
    const extras = (link.extra || []).filter((x) => x.url && x.url !== url);
    const box = $('extraLinks');
    if (!box) return;
    if (!extras.length) { box.hidden = true; box.innerHTML = ''; return; }
    box.hidden = false;
    box.innerHTML = extras.map((x) => {
        const cmd = /ffmpeg|命令/i.test(x.label || '');
        const hint = x.hint || '';
        const tip = hint ? ` class="lab" data-hint="${escHtml(hint)}" title="${escHtml(hint)}"` : '';
        return `<div class="link-row${cmd ? ' cmd-row' : ''}"><span${tip}>${escHtml(x.label)}</span><code${cmd ? ' class="cmd"' : ''}>${escHtml(x.url || '')}</code><button class="ghost" type="button">复制</button></div>`;
    }).join('');
    box.querySelectorAll('.link-row').forEach((row) => {
        const code = row.querySelector('code');
        const btn = row.querySelector('button');
        if (btn && code) btn.onclick = (e) => { e.preventDefault(); copyText(code.textContent, btn); };
    });
}

function renderMediaStats(s) {
    s = s || {};
    const wxh = (s.width && s.height) ? (s.width + 'x' + s.height) : '-';
    const rec = [s.isRecordingHLS ? 'HLS' : '', s.isRecordingMP4 ? 'MP4' : ''].filter(Boolean).join(' / ') || '关';
    const groups = [
        ['概览', [['状态', s.status === 'wait' ? '等待' : '活跃'], ['拉流客户端', s.totalReaderCount || 0], ['推流时长', fmtDuration(s.aliveSecond)], ['来源', s.originTypeStr || '-'], ['推流端', s.origin_peer || '-'], ['录制', rec]]],
        ['视频', [['编码', s.video_codec || '-'], ['分辨率', wxh], ['帧率', s.fps || '-'], ['GOP', fmtGop(s)], ['视频码率', fmtBits(s.video_bps)]]],
        ['音频', [['编码', s.audio_codec || '-'], ['采样率', s.sample_rate || '-'], ['声道', s.channels || '-'], ['位深', s.sample_bit || '-'], ['音频码率', fmtBits(s.audio_bps)], ['源流 A-V', (Number(s.av_diff_ms || 0).toFixed(0)) + ' ms']]],
        ['网络', [['网络输入', fmtBits(s.in_bps || s.bytesSpeed)], ['网络输出', fmtBits(s.out_bps)], ['读取大小', fmtBytes(s.read_size || s.in_bytes)]]]
    ];
    const cell = (lab, val) => `<div class="stat"><div class="lab">${lab}</div><div class="val">${val}</div></div>`;
    if ($('mediaStats')) $('mediaStats').innerHTML = groups.map(([title, items]) =>
        `<div class="media-group"><h4>${title}</h4><div class="media-stats">${items.map(([a, b]) => cell(a, b)).join('')}</div></div>`
    ).join('');
}

function setPlayStatus(msg, cls) {
    const el = $('playStatus');
    if (!el) return;
    el.textContent = msg;
    el.className = 'player-status' + (cls ? ' ' + cls : '');
}

function isHevcStream() {
    const c = String((current && current.media && (current.media.video_codec || current.media.codecIdName || '')) || '').toUpperCase();
    return c.indexOf('265') >= 0 || c.indexOf('HEVC') >= 0 || c.indexOf('HVC1') >= 0 || c.indexOf('HEV1') >= 0;
}

function canPlayHevcMse() {
    const ms = window.ManagedMediaSource || window.MediaSource;
    if (!ms || !ms.isTypeSupported) return false;
    return [
        'video/mp4; codecs="hvc1.1.6.L120.B0"',
        'video/mp4; codecs="hev1.1.6.L120.B0"',
        'video/mp4; codecs="hvc1.1.6.L93.B0"',
        'video/mp4; codecs="hev1.1.6.L93.B0"',
        'video/mp4; codecs="hvc1"',
        'video/mp4; codecs="hev1"',
        'video/mp2t; codecs="hvc1.1.6.L120.B0"'
    ].some((m) => { try { return ms.isTypeSupported(m); } catch (e) { return false; } });
}

function fmp4MimeList(preferHevc) {
    const hevc = preferHevc == null ? isHevcStream() : !!preferHevc;
    const list = hevc
        ? [
            'video/mp4; codecs="hvc1.1.6.L120.B0, mp4a.40.2"',
            'video/mp4; codecs="hev1.1.6.L120.B0, mp4a.40.2"',
            'video/mp4; codecs="hvc1.1.6.L93.B0"',
            'video/mp4; codecs="hev1.1.6.L93.B0"',
            'video/mp4'
        ]
        : [
            'video/mp4; codecs="avc1.640028, mp4a.40.2"',
            'video/mp4; codecs="avc1.42E01E, mp4a.40.2"',
            'video/mp4; codecs="avc1.42E01E"',
            'video/mp4'
        ];
    if (!window.MediaSource || !MediaSource.isTypeSupported) return [];
    return list.filter((m) => MediaSource.isTypeSupported(m));
}

function mseAlive(gen, ms, sb) {
    return gen === playGen && ms && ms.readyState === 'open' && sb;
}

function fallbackFromFmp4(reason) {
    if (!current || current._fmp4Fallback) return false;
    const urls = current.urls || {};
    if (urls['http-flv']) {
        current._fmp4Fallback = true;
        setPlayStatus((reason || 'fMP4 不可播') + '，已改用 HTTP-FLV', 'warn');
        playProto('http-flv');
        return true;
    }
    if (urls.hls) {
        current._fmp4Fallback = true;
        setPlayStatus((reason || 'fMP4 不可播') + '，已改用 HLS', 'warn');
        playProto('hls');
        return true;
    }
    return false;
}

function bindPlayObject(v, ms) {
    if (playObjectUrl) {
        try { URL.revokeObjectURL(playObjectUrl); } catch (e) { }
        playObjectUrl = '';
    }
    playObjectUrl = URL.createObjectURL(ms);
    v.src = playObjectUrl;
}

function destroyPlayer() {
    playGen += 1;
    if (mseAbort) { try { mseAbort.abort(); } catch (e) { } mseAbort = null; }
    if (wsFmp4) { try { wsFmp4.close(); } catch (e) { } wsFmp4 = null; }
    if (dashPlayer) {
        try { dashPlayer.reset(); } catch (e) { }
        try { dashPlayer.destroy(); } catch (e) { }
        dashPlayer = null;
    }
    if (hlsPlayer) { try { hlsPlayer.destroy(); } catch (e) { } hlsPlayer = null; }
    if (mpegtsPlayer) { try { mpegtsPlayer.destroy(); } catch (e) { } mpegtsPlayer = null; }
    if (rtcPC) { try { rtcPC.close(); } catch (e) { } rtcPC = null; }
    const hint = $('vlcHint');
    if (hint) hint.classList.remove('show');
    const v = $('video');
    if (!v) return;
    v.style.visibility = 'visible';
    v.pause();
    v.srcObject = null;
    if (playObjectUrl) {
        try { URL.revokeObjectURL(playObjectUrl); } catch (e) { }
        playObjectUrl = '';
    }
    v.removeAttribute('src');
    try { v.load(); } catch (e) { }
}

function playProto(proto) {
    destroyPlayer();
    const link = findLink(proto);
    const urls = (current && current.urls) || {};
    current.activeUrl = link.url || urls[proto] || '';
    current.activeProto = proto;
    current._hlsFallback = false;
    if (proto !== 'http-flv' && proto !== 'hls') current._fmp4Fallback = false;
    document.querySelectorAll('#protoTabs button').forEach((b) => b.classList.toggle('active', b.dataset.proto === proto));
    renderLinkPanel(link);
    const vlc = $('vlcHint');
    const video = $('video');
    if (video) {
        const wrap = video.closest('.player-video-wrap');
        if (wrap) wrap.classList.add('show-url');
    }
    if (!link.web_play) {
        if (video) video.style.visibility = 'hidden';
        if (vlc) vlc.classList.add('show');
        const title = $('vlcHintTitle');
        const body = $('vlcHintBody');
        if (proto === 'dash') {
            if (title) title.textContent = 'ZLM 不直接输出 DASH';
            if (body) body.textContent = '在 ZLM 机器执行下方 FFmpeg 命令后，用 ffplay 打开顶部 MPD 地址';
            setPlayStatus('请复制 FFmpeg 命令在节点上转封装，再用 ffplay 播放 MPD', 'warn');
        } else if (proto === 'gb28181') {
            if (title) title.textContent = '浏览器不支持国标 SIP';
            if (body) body.textContent = '由 GB28181 平台向该 SIP 口 INVITE 拉流；播放地址与流 ID 见画面顶部和下方';
            setPlayStatus('请复制 SIP / 流 ID 给国标平台', 'warn');
        } else {
            if (title) title.textContent = '浏览器不支持该协议预览播放';
            if (body) body.textContent = '请复制画面顶部播放地址，用 VLC / ffplay 打开：媒体 → 打开网络串流';
            setPlayStatus('浏览器不支持 ' + (link.label || proto) + ' 预览，请复制顶部地址用 VLC / ffplay', 'warn');
        }
        return;
    }
    video.style.visibility = 'visible';
    if (vlc) vlc.classList.remove('show');
    const url = displayedMediaUrl(current.activeUrl);
    current.activeUrl = url || current.activeUrl;
    if (!assertPublicPlayUrl(url || current.activeUrl) && proto !== 'webrtc') return;
    if (proto === 'http-flv') return playMpegts('flv', url, 'HTTP-FLV');
    if (proto === 'ws-flv') return playMpegts('flv', url, 'WS-FLV');
    if (proto === 'http-ts') return playMpegts('mpegts', url, 'HTTP-TS');
    if (proto === 'ws-ts') return playMpegts('mpegts', url, 'WS-TS');
    if (proto === 'hls' || proto === 'hls-fmp4') return playHls(url, (link.label || proto).toUpperCase());
    if (proto === 'http-fmp4') return playFmp4(url);
    if (proto === 'ws-fmp4') return playWsFmp4(url);
    if (proto === 'dash') return playDash(url);
    if (proto === 'webrtc') return playZlmWebRTC(url, link);
    video.src = url; video.play();
}

function playMpegts(type, url, label) {
    setPlayStatus('连接中 ' + label + ' ...');
    if (typeof mpegts === 'undefined' || !mpegts.isSupported()) {
        setPlayStatus('当前浏览器不支持 mpegts.js', 'error');
        return;
    }
    const playUrl = url;
    const player = mpegts.createPlayer({ type, url: playUrl, isLive: true, enableWorker: true, liveBufferLatencyChasing: true, withCredentials: true });
    player.attachMediaElement($('video'));
    player.load();
    player.play().then(() => setPlayStatus('正在播放 ' + label, 'ok')).catch((e) => setPlayStatus('播放失败: ' + e.message, 'error'));
    player.on(mpegts.Events.ERROR, (t, d, e) => {
        const msg = (e && (e.msg || e.message)) || d || t || 'unknown';
        setPlayStatus('播放错误: ' + msg, 'error');
    });
    mpegtsPlayer = player;
}

function dashEnabled() {
    if (current && current.enableDash != null) return !!current.enableDash;
    return !!(document.body && document.body.dataset.dash === '1');
}

function showDashHint(msg) {
    const video = $('video');
    const vlc = $('vlcHint');
    if (video) video.style.visibility = 'hidden';
    if (vlc) vlc.classList.add('show');
    if ($('vlcHintTitle')) $('vlcHintTitle').textContent = 'DASH 暂不可播';
    if ($('vlcHintBody')) $('vlcHintBody').textContent = msg || '请复制下方 FFmpeg 命令，或到配置页开启 DASH 自动转封装';
    setPlayStatus(msg || 'DASH 拉流失败', 'warn');
}

async function playDash(url) {
    setPlayStatus('正在尝试拉 DASH ...');
    const video = $('video');
    const vlc = $('vlcHint');
    if (video) video.style.visibility = 'visible';
    if (vlc) vlc.classList.remove('show');
    try {
        const auto = dashEnabled() && current;
        if (auto) {
            setPlayStatus('已开启 DASH，正在转封装并尝试拉流 ...');
            fetch('/api/node/' + encodeURIComponent(current.nodeId) + '/dash_ensure?vhost=' + encodeURIComponent(current.vhost || '') + '&app=' + encodeURIComponent(current.app || '') + '&stream=' + encodeURIComponent(current.stream || '')).catch(() => { });
        }
        let ok = false;
        const tries = auto ? 25 : 5;
        for (let i = 0; i < tries; i++) {
            try {
                const r = await fetch(url, { cache: 'no-store', credentials: 'include' });
                const t = r.ok ? await r.text() : '';
                if (r.ok && t.indexOf('MPD') >= 0) { ok = true; break; }
            } catch (e) { }
            await new Promise((res) => setTimeout(res, 400));
        }
        if (!ok) {
            showDashHint(auto
                ? 'DASH 还没有 MPD。请确认本机 ffmpeg 可拉 RTMP，或复制下方命令手动执行'
                : 'DASH 还没有 MPD。请到配置页开启自动转封装，或复制下方命令手动执行');
            return;
        }
        if (typeof dashjs === 'undefined' || !dashjs.MediaPlayer) {
            showDashHint('未加载 dash.js，请用 ffplay 打开顶部 MPD 地址');
            return;
        }
        const player = dashjs.MediaPlayer().create();
        dashPlayer = player;
        if (player.setXHRWithCredentialsForType) {
            player.setXHRWithCredentialsForType('MPD', true);
            player.setXHRWithCredentialsForType('MediaSegment', true);
            player.setXHRWithCredentialsForType('InitializationSegment', true);
        }
        player.initialize(video, url, true);
        setPlayStatus('正在播放 DASH', 'ok');
        player.on(dashjs.MediaPlayer.events.ERROR, (e) => {
            const err = (e && e.error && (e.error.message || e.error.code)) || 'error';
            setPlayStatus('DASH 错误: ' + err, 'error');
            if (video && video.readyState < 2) {
                showDashHint('DASH 拉流失败: ' + err + '。可复制下方命令在节点上重新生成');
            }
        });
    } catch (e) {
        showDashHint('DASH 预览失败: ' + ((e && e.message) || e));
    }
}

function playlistHasHevc(text) {
    return /CODECS="[^"]*(hvc1|hev1|hev1|h265|hevc)/i.test(String(text || ''));
}

async function inspectPublicHls(url) {
    const r = await fetch(url, { cache: 'no-store', credentials: 'omit', mode: 'cors' });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const text = await r.text();
    const map = /#EXT-X-MAP:URI="([^"]+)"/i.exec(text);
    if (map) {
        const initUrl = new URL(map[1], url).href;
        const ir = await fetch(initUrl, { cache: 'no-store', credentials: 'omit', mode: 'cors' });
        if (!ir.ok) {
            if (current && current.nodeId) {
                await fetch('/api/node/' + encodeURIComponent(current.nodeId) + '/hls_init?vhost=' + encodeURIComponent(current.vhost || '') + '&app=' + encodeURIComponent(current.app || '') + '&stream=' + encodeURIComponent(current.stream || ''), { cache: 'no-store' }).catch(() => { });
                const retry = await fetch(initUrl, { cache: 'no-store', credentials: 'omit', mode: 'cors' });
                if (retry.ok) return { text, hevc: playlistHasHevc(text) };
            }
            throw new Error('init.mp4 HTTP ' + ir.status + ' ' + initUrl);
        }
    }
    return { text, hevc: playlistHasHevc(text) };
}

function playHls(url, label) {
    void playHlsAsync(url, label);
}

async function playHlsAsync(url, label) {
    url = displayedMediaUrl(url);
    setPlayStatus('正在拉 ' + label + ' : ' + url);
    const v = $('video');
    const gen = playGen;
    const hevcMeta = isHevcStream();
    const useNative = !(typeof Hls !== 'undefined' && Hls.isSupported()) && !!(v.canPlayType && v.canPlayType('application/vnd.apple.mpegurl'));
    if (hevcMeta && !canPlayHevcMse() && !useNative) {
        setPlayStatus('当前浏览器无法解码 H.265 HLS（用户用同一 m3u8 在 Chrome/Edge 同样黑屏）。地址: ' + url, 'error');
        return;
    }
    try {
        const info = await inspectPublicHls(url);
        if (gen !== playGen) return;
        if ((info.hevc || hevcMeta) && !canPlayHevcMse() && !useNative) {
            setPlayStatus('当前浏览器无法解码 H.265 HLS（用户用同一 m3u8 在 Chrome/Edge 同样黑屏）。地址: ' + url, 'error');
            return;
        }
    } catch (e) {
        if (gen !== playGen) return;
        if (current && current.activeProto === 'hls-fmp4' && !current._hlsFallback) {
            const fb = String(url).replace(/hls\.fmp4\.m3u8(\?.*)?$/i, 'hls.m3u8$1');
            if (fb !== url) {
                current._hlsFallback = true;
                setPlayStatus('HLS-fMP4 公网地址失败，改用展示的 HLS 地址 ...', 'warn');
                playHls(fb, 'HLS');
                return;
            }
        }
        setPlayStatus(label + ' 公网地址不可播: ' + ((e && e.message) || e) + ' 地址: ' + url, 'error');
        return;
    }
    const hevcHint = () => hevcMeta
        ? ' 编码是 H.265，Chrome/Edge 打开该 m3u8 通常也是黑屏。'
        : '';
    const noteBlank = () => {
        if (gen !== playGen) return;
        if (v.videoWidth > 0) {
            setPlayStatus('正在播放 ' + label, 'ok');
            return;
        }
        setPlayStatus(label + ' 地址已打开但无画面。' + hevcHint() + ' 地址: ' + url, 'warn');
    };
    if (typeof Hls !== 'undefined' && Hls.isSupported()) {
        const hls = new Hls({
            liveDurationInfinity: true,
            enableWorker: true,
            lowLatencyMode: false,
            liveSyncDurationCount: 3,
            preferManagedMediaSource: !!window.ManagedMediaSource,
            xhrSetup: (xhr) => { xhr.withCredentials = false; }
        });
        hlsPlayer = hls;
        hls.loadSource(url);
        hls.attachMedia(v);
        hls.on(Hls.Events.MANIFEST_PARSED, () => {
            v.play().then(() => {
                setPlayStatus('已打开 ' + label + '，等待出画 ...');
                setTimeout(noteBlank, 2200);
            }).catch((e) => setPlayStatus('播放失败: ' + e.message + ' 地址: ' + url, 'error'));
        });
        hls.on(Hls.Events.ERROR, (_, data) => {
            if (!data || !data.fatal) return;
            if (current && current.activeProto === 'hls-fmp4' && !current._hlsFallback) {
                const fb = String(url).replace(/hls\.fmp4\.m3u8(\?.*)?$/i, 'hls.m3u8$1');
                if (fb !== url) {
                    current._hlsFallback = true;
                    setPlayStatus('HLS-fMP4 失败，改用展示的 HLS-TS 地址 ...', 'warn');
                    destroyPlayer();
                    playHls(fb, 'HLS');
                    return;
                }
            }
            let extra = '';
            if (data.response && data.response.code) extra = ' HTTP ' + data.response.code;
            setPlayStatus('HLS 失败: ' + (data.details || data.type) + extra + hevcHint() + ' 地址: ' + url, 'error');
        });
        return;
    }
    if (v.canPlayType('application/vnd.apple.mpegurl')) {
        v.src = url;
        v.addEventListener('loadedmetadata', () => {
            v.play().then(() => {
                setPlayStatus('已打开 ' + label + '（系统播放器），等待出画 ...');
                setTimeout(noteBlank, 2200);
            });
        }, { once: true });
        return;
    }
    setPlayStatus('当前浏览器不支持 HLS。地址: ' + url, 'error');
}

function attachFmp4Source(label, onReady) {
    const gen = playGen;
    const v = $('video');
    const hevc = isHevcStream();
    const mimes = fmp4MimeList(hevc);
    if (hevc && !mimes.some((m) => /hvc1|hev1/i.test(m))) {
        if (fallbackFromFmp4('当前浏览器 MSE 不支持 H.265 fMP4')) return null;
        setPlayStatus('当前浏览器不支持 H.265 fMP4', 'error');
        return null;
    }
    const mime = mimes[0];
    if (!mime) {
        if (fallbackFromFmp4('浏览器不支持该 fMP4 编码')) return null;
        setPlayStatus('浏览器不支持该 fMP4 编码', 'error');
        return null;
    }
    const ms = new MediaSource();
    bindPlayObject(v, ms);
    ms.addEventListener('sourceopen', () => {
        if (gen !== playGen) return;
        let sb;
        try { sb = ms.addSourceBuffer(mime); } catch (e) {
            if (fallbackFromFmp4('MSE 失败: ' + e.message)) return;
            setPlayStatus('MSE 失败: ' + e.message, 'error');
            return;
        }
        try { sb.mode = 'sequence'; } catch (e) { }
        const queue = [];
        let appending = false;
        const pump = () => {
            if (appending || !queue.length || sb.updating || !mseAlive(gen, ms, sb)) return;
            appending = true;
            try {
                sb.appendBuffer(queue.shift());
            } catch (e) {
                appending = false;
                if (gen !== playGen) return;
                const msg = (e && e.message) || String(e);
                if (/removed from the parent media source/i.test(msg)) return;
                if (hevc && fallbackFromFmp4('H.265 fMP4 无法写入缓冲')) return;
                setPlayStatus('append 失败: ' + msg, 'error');
            }
        };
        sb.addEventListener('updateend', () => {
            appending = false;
            if (gen !== playGen) return;
            pump();
            if (v.paused && v.readyState >= 2) {
                v.play().then(() => setPlayStatus('正在播放 ' + label, 'ok')).catch(() => { });
            }
        });
        sb.addEventListener('error', () => {
            if (gen !== playGen) return;
            if (hevc && fallbackFromFmp4('H.265 fMP4 缓冲出错')) return;
        });
        onReady({ gen, ms, sb, queue, pump });
    }, { once: true });
    return { gen, ms };
}

function playWsFmp4(url) {
    setPlayStatus('连接中 WS-fMP4 ...');
    if (!window.MediaSource) { setPlayStatus('当前浏览器不支持 MSE / fMP4', 'error'); return; }
    const ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    wsFmp4 = ws;
    attachFmp4Source('WS-fMP4', ({ gen, queue, pump }) => {
        ws.onmessage = (ev) => {
            if (gen !== playGen || !ev.data) return;
            queue.push(ev.data);
            pump();
        };
        ws.onopen = () => { if (gen === playGen) setPlayStatus('正在播放 WS-fMP4', 'ok'); };
        ws.onerror = () => { if (gen === playGen) setPlayStatus('WS-fMP4 错误', 'error'); };
        ws.onclose = () => { if (wsFmp4 === ws) wsFmp4 = null; };
    });
}

function playFmp4(url) {
    setPlayStatus('连接中 HTTP-fMP4 ...');
    if (!window.MediaSource) { setPlayStatus('当前浏览器不支持 MSE / fMP4', 'error'); return; }
    mseAbort = new AbortController();
    const ac = mseAbort;
    attachFmp4Source('HTTP-fMP4', ({ gen, ms, queue, pump }) => {
        fetch(url, { signal: ac.signal, credentials: 'include' }).then((resp) => {
            if (gen !== playGen) return;
            if (!resp.ok) throw new Error('HTTP ' + resp.status);
            setPlayStatus('缓冲中...');
            const reader = resp.body.getReader();
            const read = () => reader.read().then(({ done, value }) => {
                if (gen !== playGen) return;
                if (done) {
                    try { if (ms.readyState === 'open') ms.endOfStream(); } catch (e) { }
                    return;
                }
                queue.push(value.buffer);
                pump();
                return read();
            });
            return read();
        }).catch((e) => {
            if (gen !== playGen || (e && e.name === 'AbortError')) return;
            if (isHevcStream() && fallbackFromFmp4('fMP4 错误: ' + ((e && e.message) || e))) return;
            setPlayStatus('fMP4 错误: ' + ((e && e.message) || e), 'error');
        });
    });
}

async function playZlmWebRTC(playUrl, link) {
    const signal = webrtcSignalUrl(playUrl || (current && current.activeUrl), link || findLink('webrtc'));
    if (!signal) {
        setPlayStatus('没有 WebRTC 信令地址', 'error');
        return;
    }
    if (!assertPublicPlayUrl(signal)) return;
    setPlayStatus('正在用展示的信令地址协商: ' + signal);
    const v = $('video');
    const pc = newRtcPeer();
    rtcPC = pc;
    const remote = new MediaStream();
    v.srcObject = remote;
    const tune = (ev) => {
        applyRtcLowDelay(pc, v);
        try { if (ev && ev.receiver) applyRtcLowDelay({ getReceivers: () => [ev.receiver] }, v); } catch (e) { }
        if (ev && ev.track) remote.addTrack(ev.track);
        if (v.paused) v.play().catch(() => { });
        setPlayStatus('正在播放 WebRTC', 'ok');
    };
    pc.ontrack = tune;
    pc.oniceconnectionstatechange = () => {
        if (pc.iceConnectionState === 'failed' || pc.iceConnectionState === 'disconnected') {
            setPlayStatus('WebRTC 断开: ' + pc.iceConnectionState, 'error');
        }
    };
    pc.addTransceiver('video', { direction: 'recvonly' });
    pc.addTransceiver('audio', { direction: 'recvonly' });
    try {
        const offer = await pc.createOffer({ offerToReceiveAudio: true, offerToReceiveVideo: true });
        await pc.setLocalDescription(offer);
        await waitHostIce(pc, 80);
        const resp = await fetch(signal, {
            method: 'POST',
            headers: { 'Content-Type': 'text/plain;charset=utf-8' },
            body: dropRelayIceSdp(pc.localDescription.sdp),
            credentials: 'omit'
        });
        const text = await resp.text();
        let ret = {};
        try { ret = JSON.parse(text); } catch (e) { throw new Error('信令不是 JSON: ' + text.slice(0, 120)); }
        if (!resp.ok || (ret.code && ret.code !== 0)) throw new Error(ret.msg || ('HTTP ' + resp.status));
        const sdp = dropRelayIceSdp(ret.sdp || ret.data || '');
        if (!sdp) throw new Error('信令未返回 SDP');
        await pc.setRemoteDescription({ type: 'answer', sdp });
        applyRtcLowDelay(pc, v);
        setPlayStatus('信令完成，等待媒体 ...', 'ok');
    } catch (e) {
        setPlayStatus('WebRTC 失败: ' + e.message + ' 信令: ' + signal, 'error');
    }
}

function closePlayer() {
    destroyPlayer();
    if ($('overlay')) $('overlay').classList.remove('show');
}

function isImagePreview(path, kind, role) {
    if (kind === 'snap' || role === 'live_snap') return true;
    return /\.(jpe?g|png|gif|webp|bmp)$/i.test(String(path || ''));
}

function resetFilePreviewMedia() {
    filePlayGen += 1;
    if (fileMseAbort) { try { fileMseAbort.abort(); } catch (e) { } fileMseAbort = null; }
    if (fileHls) { try { fileHls.destroy(); } catch (e) { } fileHls = null; }
    if (fileMpegts) { try { fileMpegts.destroy(); } catch (e) { } fileMpegts = null; }
    const v = $('fileVideo');
    if (v) {
        v.pause();
        v.removeAttribute('src');
        v.srcObject = null;
        v.hidden = false;
        if (fileObjectUrl) {
            try { URL.revokeObjectURL(fileObjectUrl); } catch (e) { }
            fileObjectUrl = '';
        }
        try { v.load(); } catch (e) { }
    }
    const img = $('fileImage');
    if (img) {
        img.onload = null;
        img.onerror = null;
        img.removeAttribute('src');
        img.alt = '';
    }
    const stage = $('fileStage') || (v && v.parentElement);
    if (stage) stage.classList.remove('is-image');
}

function closeFilePreview() {
    resetFilePreviewMedia();
    if ($('fileOverlay')) $('fileOverlay').classList.remove('show');
}

function showFileImage(path) {
    const v = $('fileVideo');
    const img = $('fileImage');
    const stage = $('fileStage') || (v && v.parentElement);
    const url = mediaUrl(path) + '&t=' + Date.now();
    const name = String(path).split('/').pop() || '截图';
    if (stage) stage.classList.add('is-image');
    if (v) v.hidden = true;
    if (!img) {
        setFileStatus('无法预览图片', 'error');
        return;
    }
    img.alt = name;
    img.onload = () => setFileStatus('图片预览', 'ok');
    img.onerror = () => setFileStatus('图片无法加载: ' + url, 'error');
    img.src = url;
    setFileStatus('正在打开图片: ' + name);
}

function setFileStatus(msg, cls) {
    const el = $('filePlayStatus');
    if (!el) return;
    el.textContent = msg || '';
    el.className = 'player-status' + (cls ? ' ' + cls : '');
}

function pickPlaylist(path, file) {
    if (file && file.play_url) return file.play_url;
    if (file && file.playlist) return file.playlist;
    if (/\.m3u8$/i.test(path)) return path;
    const dir = String(path).replace(/\/[^/]+$/, '');
    const hit = (recFiles || []).find((f) => f.dir === dir && (f.ext === '.m3u8' || f.kind === 'hls'));
    return hit ? (hit.play_url || hit.playlist || hit.path) : '';
}

function liveHlsUrlFromPath(path) {
    return displayedMediaUrl(path);
}

function isLiveHlsPreview(path, role, url) {
    if (role === 'live_hls') return true;
    return /\/hls(?:\.fmp4)?\.m3u8(\?|$)/i.test(String(url || path || ''));
}

function liveFallbackUrl(hlsUrl, ext) {
    return String(hlsUrl).replace(/\/hls(?:\.fmp4)?\.m3u8(\?.*)?$/i, '.live.' + ext + '$1');
}

async function previewFile(path, kind, playlist, role) {
    const file = (recFiles || []).find((f) => f.path === path);
    role = role || (file && file.role) || '';
    kind = kind || (file && file.kind) || '';
    const list = playlist || pickPlaylist(path, file) || liveHlsUrlFromPath(path);
    const name = String(path).split('/').pop();
    if ($('fileTitle')) $('fileTitle').textContent = path;
    resetFilePreviewMedia();
    $('fileOverlay').classList.add('show');
    if (isImagePreview(path, kind, role)) {
        showFileImage(path);
        return;
    }
    const v = $('fileVideo');
    if (!v) {
        setFileStatus('播放器不可用', 'error');
        return;
    }
    const playList = async (m3u8) => {
        const url = displayedMediaUrl(m3u8) || mediaUrl(m3u8);
        const live = isLiveHlsPreview(path, role, url);
        setFileStatus((live ? '正在播放直播 HLS: ' : '正在播放列表: ') + url);
        if (window.Hls && Hls.isSupported()) {
            const gen = filePlayGen;
            fileHls = new Hls({
                enableWorker: true,
                maxBufferLength: 30,
                liveDurationInfinity: !!live,
                lowLatencyMode: false,
                liveSyncDurationCount: 2,
                preferManagedMediaSource: !!window.ManagedMediaSource,
                xhrSetup: (xhr) => { xhr.withCredentials = true; }
            });
            fileHls.on(Hls.Events.ERROR, (_, data) => {
                if (!data || !data.fatal || gen !== filePlayGen) return;
                let extra = '';
                if (data.response && data.response.code) extra = ' HTTP ' + data.response.code;
                setFileStatus('HLS 失败: ' + (data.details || data.type) + extra + ' 地址: ' + url, 'error');
            });
            fileHls.loadSource(url);
            fileHls.attachMedia(v);
            v.play().catch(() => { });
            return true;
        }
        if (v.canPlayType('application/vnd.apple.mpegurl')) {
            v.src = url;
            v.play().catch(() => { });
            return true;
        }
        return false;
    };
    const playNative = async (url, hint, opt) => {
        opt = opt || {};
        setFileStatus(hint);
        try {
            const r = await fetch(url, { headers: { Range: 'bytes=0-8' }, credentials: 'include' });
            if (!r.ok) {
                setFileStatus('播放失败: 文件不可用 HTTP ' + r.status, 'error');
                return false;
            }
        } catch (e) {
            setFileStatus('播放失败: 无法读取文件 ' + ((e && e.message) || e), 'error');
            return false;
        }
        const waitReady = (timeoutMs) => new Promise((resolve, reject) => {
            let done = false;
            const finish = (fn, arg) => {
                if (done) return;
                done = true;
                clearTimeout(t);
                v.removeEventListener('playing', onOk);
                v.removeEventListener('canplay', onOk);
                v.removeEventListener('loadedmetadata', onMeta);
                v.removeEventListener('error', onErr);
                fn(arg);
            };
            const onOk = () => finish(resolve);
            const onMeta = () => { if (v.readyState >= 1) setFileStatus('已解析索引，按 Range 快开…'); };
            const onErr = () => {
                const code = v.error && v.error.code;
                const msg = (v.error && v.error.message) || '浏览器无法解码';
                finish(reject, new Error(code === 4 ? '浏览器无法解码该封装/编码' : msg));
            };
            const t = setTimeout(() => {
                if (v.readyState >= 1) onOk();
                else finish(reject, new Error('打开超时（文件可能仍在写入）'));
            }, timeoutMs || 12000);
            v.addEventListener('playing', onOk);
            v.addEventListener('canplay', onOk);
            v.addEventListener('loadedmetadata', onMeta);
            v.addEventListener('error', onErr);
        });
        try {
            v.preload = 'auto';
            v.src = url;
            const p = v.play();
            if (p && p.catch) p.catch(() => { });
            await waitReady(opt.timeoutMs || 12000);
            setFileStatus('正在播放', 'ok');
            return true;
        } catch (e) {
            if (opt.noMseFallback) {
                if (v.readyState >= 1) {
                    setFileStatus('已打开，等待出画…', 'warn');
                    return true;
                }
                setFileStatus('浏览器无法播放该录像（常见于 H.265，或 MP4 仍在写入 / moov 在文件尾）。请加载为点播流后预览，或下载后用 VLC 打开。', 'error');
                return false;
            }
        }
        if (await playFileMp4Mse(url)) return true;
        setFileStatus('浏览器无法播放该录像（常见于 H.265，或 MP4 仍在写入 / moov 在文件尾）。请加载为点播流后预览，或下载后用 VLC 打开。', 'error');
        return false;
    };
    const playFileMp4Mse = (url) => new Promise((resolve) => {
        if (!window.MediaSource) { resolve(false); return; }
        const gen = ++filePlayGen;
        const mimes = fmp4MimeList(true).concat(fmp4MimeList(false).filter((m) => m !== 'video/mp4'));
        const mime = mimes.find((m) => MediaSource.isTypeSupported(m));
        if (!mime) { resolve(false); return; }
        if (fileMseAbort) { try { fileMseAbort.abort(); } catch (e) { } }
        fileMseAbort = new AbortController();
        const ac = fileMseAbort;
        const ms = new MediaSource();
        if (fileObjectUrl) { try { URL.revokeObjectURL(fileObjectUrl); } catch (e) { } }
        fileObjectUrl = URL.createObjectURL(ms);
        v.src = fileObjectUrl;
        let settled = false;
        const done = (ok) => { if (settled) return; settled = true; resolve(!!ok); };
        ms.addEventListener('sourceopen', () => {
            if (gen !== filePlayGen) { done(false); return; }
            let sb;
            try { sb = ms.addSourceBuffer(mime); } catch (e) { done(false); return; }
            try { sb.mode = 'sequence'; } catch (e) { }
            const queue = [];
            let appending = false;
            const pump = () => {
                if (appending || !queue.length || sb.updating || gen !== filePlayGen || ms.readyState !== 'open') return;
                appending = true;
                try { sb.appendBuffer(queue.shift()); } catch (e) { appending = false; done(false); }
            };
            sb.addEventListener('updateend', () => {
                appending = false;
                pump();
                if (!settled && v.readyState >= 2) {
                    v.play().then(() => { setFileStatus('正在播放', 'ok'); done(true); }).catch(() => { });
                }
            });
            fetch(url, { signal: ac.signal, credentials: 'include' }).then((resp) => {
                if (!resp.ok) throw new Error('HTTP ' + resp.status);
                const reader = resp.body.getReader();
                const read = () => reader.read().then(({ done: eof, value }) => {
                    if (gen !== filePlayGen) return;
                    if (eof) {
                        try { if (ms.readyState === 'open') ms.endOfStream(); } catch (e) { }
                        setTimeout(() => { if (!settled) done(v.readyState >= 2); }, 800);
                        return;
                    }
                    queue.push(value.buffer);
                    pump();
                    return read();
                });
                return read();
            }).catch((e) => {
                if (e && e.name === 'AbortError') { done(false); return; }
                done(false);
            });
        }, { once: true });
        setTimeout(() => { if (!settled) done(v.readyState >= 2); }, 6000);
    });
    const needList = (/\.m3u8$/i.test(path) || /\.mpd$/i.test(path) || kind === 'hls' || kind === 'dash');
    try {
        if (needList && list) { await playList(list); return; }
        if (kind === 'hls' || /\.m3u8$/i.test(path)) { await playList(path); return; }
        if (/\.mpd$/i.test(path) || /^init(\.mp4|-stream)/i.test(name) || /\.m4s$/i.test(path) || (kind === 'fmp4' && /\.mp4$/i.test(path) && !/rec/i.test(role))) {
            if (list && /\.m3u8$/i.test(list)) { await playList(list); return; }
            const concat = `/api/node/${encodeURIComponent(nodeId())}/fmp4-list?path=${encodeURIComponent(path)}`;
            setFileStatus('正在拼接 init + 分片 ...');
            await playList(concat);
            return;
        }
        if ((kind === 'ts' || /\.ts$/i.test(path)) && typeof mpegts !== 'undefined' && mpegts.isSupported()) {
            setFileStatus('正在播放 TS');
            fileMpegts = mpegts.createPlayer({ type: 'mpegts', url: mediaUrl(path), isLive: false, enableWorker: true });
            fileMpegts.attachMediaElement(v);
            fileMpegts.load();
            fileMpegts.play().catch(() => { });
            return;
        }
        await playNative(mediaUrl(path), '正在播放 ' + (kind || '文件'), {
            noMseFallback: true,
            timeoutMs: 12000
        });
    } catch (e) {
        setFileStatus('预览失败: ' + e.message, 'error');
    }
}

function chartTheme() {
    return {
        text: cssVar('--text', '#e7f0df'),
        muted: cssVar('--muted', '#8aa07a'),
        line: cssVar('--line', '#2c3a28'),
        panel: cssVar('--el-bg-color', '#1a2118')
    };
}

function fmtChartTime(t) {
    const d = new Date((Number(t) || 0) * 1000);
    return (d.getMonth() + 1) + '-' + d.getDate() + ' ' + String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
}

function fmtChartAxis(tsMs, range) {
    const d = new Date(tsMs);
    const md = (d.getMonth() + 1) + '-' + d.getDate();
    const hm = String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
    if (range === '1h') return hm;
    if (range === '1d') return hm;
    return md + '\n' + hm;
}

function fmtChartY(v, extra) {
    if (extra && typeof extra.yfmt === 'function') return extra.yfmt(v);
    const n = Number(v) || 0;
    if (extra && extra.max === 100) return (Math.round(n * 10) / 10) + '%';
    if (n >= 1000 || (extra && extra.bps)) {
        if (n >= 1e9) return (n / 1e9).toFixed(1) + 'G';
        if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
        if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
    }
    return String(Math.round(n * 10) / 10);
}

function bindChartResize() {
    if (window._chartResizeBound) return;
    window._chartResizeBound = true;
    window.addEventListener('resize', () => {
        if (typeof echarts === 'undefined') return;
        document.querySelectorAll('.chart-box').forEach((el) => {
            const inst = echarts.getInstanceByDom(el);
            if (inst) inst.resize();
        });
    });
}

function renderChart(id, series, extra) {
    const el = $(id);
    if (!el || typeof echarts === 'undefined') return;
    bindChartResize();
    extra = extra || {};
    const th = chartTheme();
    const pts = (series[0] && series[0].data) || [];
    const fromMs = extra.fromMs || (pts.length ? pts[0].t * 1000 : Date.now());
    const toMs = extra.toMs || (pts.length ? pts[pts.length - 1].t * 1000 : fromMs);
    let inst = echarts.getInstanceByDom(el);
    if (!inst) inst = echarts.init(el, null, { renderer: 'canvas' });
    inst.setOption({
        backgroundColor: 'transparent',
        animationDuration: 240,
        textStyle: { color: th.muted, fontSize: 11 },
        tooltip: {
            trigger: 'axis',
            backgroundColor: th.panel,
            borderColor: th.line,
            textStyle: { color: th.text, fontSize: 12 },
            formatter: (items) => {
                if (!items || !items.length) return '';
                const ts = Array.isArray(items[0].value) ? items[0].value[0] : items[0].axisValue;
                return '<b>' + fmtChartTime(ts / 1000) + '</b>' + items.map((it) => {
                    const raw = Array.isArray(it.value) ? it.value[1] : it.value;
                    const s = series[it.seriesIndex] || {};
                    const val = s.fmt ? s.fmt(raw) : String(Math.round(raw * 10) / 10);
                    return '<div><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:' + (s.color || it.color) + ';margin-right:6px"></span>' + (s.label || it.seriesName || '') + ' <b>' + val + '</b></div>';
                }).join('');
            }
        },
        grid: { left: extra.yPad || 12, right: 16, top: 28, bottom: extra.xPad || 36, containLabel: true },
        xAxis: {
            type: 'time',
            min: fromMs,
            max: toMs,
            axisLine: { lineStyle: { color: th.line } },
            axisLabel: {
                color: th.muted,
                hideOverlap: true,
                formatter: (val) => fmtChartAxis(val, extra.range)
            },
            splitLine: { show: false }
        },
        yAxis: {
            type: 'value',
            min: 0,
            max: extra.max,
            minInterval: extra.minInterval,
            scale: false,
            name: extra.yName || '',
            nameLocation: 'end',
            nameGap: 8,
            nameTextStyle: { color: th.muted, fontSize: 11, align: 'left', padding: [0, 0, 0, 0] },
            axisLabel: { color: th.muted, hideOverlap: true, formatter: (v) => fmtChartY(v, extra) },
            splitLine: { lineStyle: { color: th.line, type: 'dashed' } }
        },
        series: series.map((s) => ({
            name: s.label,
            type: 'line',
            showSymbol: false,
            smooth: 0.15,
            data: (s.data || []).map((p) => [p.t * 1000, p.v]),
            lineStyle: { color: s.color, width: 1.6 },
            itemStyle: { color: s.color }
        }))
    }, true);
}

async function loadCharts() {
    try {
        const d = await (await fetch('/api/history?range=' + encodeURIComponent(chartRange))).json();
        const pts = d.points || [];
        const axis = { range: d.range || chartRange, fromMs: (d.from || 0) * 1000, toMs: (d.to || 0) * 1000 };
        const num = (v) => String(Math.round((v || 0) * 10) / 10);
        const pct = (v) => (Math.round((v || 0) * 10) / 10) + '%';
        renderChart('chartFlow', [
            { color: '#409eff', label: '推流数', data: pts.map((p) => ({ t: p.t, v: p.push || 0 })), fmt: num },
            { color: '#67c23a', label: '拉流数', data: pts.map((p) => ({ t: p.t, v: p.pull || 0 })), fmt: num },
            { color: '#909399', label: '连接数', data: pts.map((p) => ({ t: p.t, v: p.conn || 0 })), fmt: num }
        ], Object.assign({ minInterval: 1 }, axis));
        renderChart('chartHost', [
            { color: '#e6a23c', label: 'CPU', data: pts.map((p) => ({ t: p.t, v: p.cpu || 0 })), fmt: pct },
            { color: '#67c23a', label: '内存', data: pts.map((p) => ({ t: p.t, v: p.mem || 0 })), fmt: pct },
            { color: '#409eff', label: '网络', data: pts.map((p) => ({ t: p.t, v: p.net_util || 0 })), fmt: pct }
        ], Object.assign({ max: 100 }, axis));
        const fmtBps = (v) => {
            const n = Number(v) || 0;
            if (n >= 1e9) return (n / 1e9).toFixed(1) + ' GB/s';
            if (n >= 1e6) return (n / 1e6).toFixed(1) + ' MB/s';
            if (n >= 1e3) return (n / 1e3).toFixed(1) + ' KB/s';
            return Math.round(n) + ' B/s';
        };
        const inBits = pts.map((p) => ({ t: p.t, v: (Number(p.in_bps) || 0) * 8 }));
        const outBits = pts.map((p) => ({ t: p.t, v: (Number(p.out_bps) || 0) * 8 }));
        const mediaAxis = bitAxis(inBits.concat(outBits).map((p) => p.v));
        renderChart('chartBitrate', [
            { color: '#67c23a', label: '入口', data: inBits, fmt: (v) => fmtBits(v / 8) },
            { color: '#409eff', label: '出口', data: outBits, fmt: (v) => fmtBits(v / 8) }
        ], Object.assign({ yName: mediaAxis.name, yfmt: mediaAxis.yfmt }, axis));
        renderChart('chartNet', [
            { color: '#67c23a', label: '下行', data: pts.map((p) => ({ t: p.t, v: p.net_rx || 0 })), fmt: fmtBps },
            { color: '#409eff', label: '上行', data: pts.map((p) => ({ t: p.t, v: p.net_tx || 0 })), fmt: fmtBps }
        ], Object.assign({ bps: true, yfmt: fmtBps, containLabel: true }, axis));
    } catch (e) { }
}

function fillDeviceSelect(sel, devices, label) {
    const cur = sel.value;
    sel.innerHTML = '';
    const def = document.createElement('option');
    def.value = '';
    def.textContent = devices.length ? ('默认' + label) : ('未检测到' + label);
    sel.appendChild(def);
    devices.forEach((d, i) => {
        if (!d.deviceId) return;
        const o = document.createElement('option');
        o.value = d.deviceId;
        o.textContent = d.label || (label + ' ' + (i + 1));
        sel.appendChild(o);
    });
    if (cur && [...sel.options].some((o) => o.value === cur)) sel.value = cur;
}
async function listPushDevices(autoPick) {
    if (!navigator.mediaDevices || !navigator.mediaDevices.enumerateDevices) return { cams: [], mics: [] };
    const all = await navigator.mediaDevices.enumerateDevices();
    const cams = all.filter((d) => d.kind === 'videoinput');
    const mics = all.filter((d) => d.kind === 'audioinput');
    if ($('pushCamId')) fillDeviceSelect($('pushCamId'), cams, '摄像头');
    if ($('pushMicId')) fillDeviceSelect($('pushMicId'), mics, '麦克风');
    if (autoPick) {
        if ($('pushCam')) $('pushCam').checked = cams.length > 0 && !($('pushScreen') && $('pushScreen').checked);
        if ($('pushMic')) $('pushMic').checked = mics.length > 0;
        if (!cams.length && $('pushScreen')) $('pushScreen').checked = true;
    }
    return { cams, mics };
}
async function preparePushPage() {
    const hint = $('pushSecureHint');
    if (!hint) return;
    const href = httpsAdminUrl();
    if (!window.isSecureContext) {
        hint.className = 'push-hint err';
        hint.innerHTML = 'Chrome 在局域网 HTTP 下会禁止摄像头。请用本后台 HTTPS 打开：<a href="' + href + '" style="color:var(--accent)">' + href + '</a> （首次需在浏览器里继续访问自签证书）';
        return;
    }
    hint.className = 'push-hint muted';
    let extra = 'HTTPS 可用。回显=先推再拉；WHEP 请用 play 粘贴，不能当视频地址打开。';
    try {
        const { cams, mics } = await listPushDevices(true);
        extra += ' 摄像头 ' + cams.length + '，麦克风 ' + mics.length + '。';
        if (!cams.length) extra += ' 未检测到摄像头，已改为屏幕/窗口。';
        else if (!mics.length) extra += ' 无麦克风，已取消勾选。';
    } catch (e) {
        extra += ' 枚举设备失败: ' + ((e && e.message) || e);
    }
    hint.textContent = extra;
    hint.title = extra;
}

function initEventFilters() {
    const sel = $('eventKind');
    if (sel && sel.options.length <= 1) {
        const names = [...new Set([...document.querySelectorAll('#eventLog .hook-row')].map((r) => r.dataset.event).filter(Boolean))].sort();
        names.forEach((n) => {
            const o = document.createElement('option');
            o.value = n;
            o.textContent = n;
            sel.appendChild(o);
        });
    }
    filterHookEvents();
}

function filterHookEvents() {
    const rows = [...document.querySelectorAll('#eventLog .hook-row')];
    if (!$('eventLog')) return;
    const kw = (($('qEvent') && $('qEvent').value) || '').trim().toLowerCase();
    const kind = ($('eventKind') && $('eventKind').value) || '';
    let shown = 0;
    rows.forEach((row) => {
        const hay = ((row.dataset.hay || '') + ' ' + row.textContent).toLowerCase();
        const ok = (!kind || row.dataset.event === kind) && (!kw || hay.includes(kw));
        row.hidden = !ok;
        if (ok) shown++;
    });
    const empty = $('eventEmpty');
    const none = $('eventNone');
    if (empty) empty.hidden = !(rows.length && shown === 0);
    if (none) none.hidden = rows.length > 0;
    if ($('eventCount')) {
        $('eventCount').textContent = rows.length ? (shown + ' / ' + rows.length) : '';
    }
}

function setPushStatus(msg) { if ($('pushStatus')) $('pushStatus').textContent = msg; }

function gumError(e) {
    const name = (e && e.name) || '';
    const msg = (e && e.message) || String(e);
    if (name === 'NotFoundError' || /device not found/i.test(msg)) {
        return '找不到摄像头/麦克风。请勾选「屏幕/窗口」改推桌面；或取消勾选没有的设备。Windows：设置 → 隐私和安全性 → 相机/麦克风，允许桌面应用访问。Chrome：地址栏锁图标 → 允许摄像头。';
    }
    if (name === 'NotAllowedError' || name === 'PermissionDeniedError') {
        return '浏览器拒绝了权限。点地址栏锁图标允许摄像头/麦克风，或改用屏幕/窗口共享。';
    }
    if (name === 'NotReadableError' || name === 'TrackStartError') {
        return '设备被其他程序占用（会议软件/OBS 等），请关闭后重试。';
    }
    if (name === 'OverconstrainedError') return '当前设备不支持所选参数，请改选设备或只勾选屏幕/窗口。';
    if (/type can not supported/i.test(msg)) {
        return '当前 ZLM 是 Release 版，不支持 type=echo。请把方式改成「echo 回显（先推再拉）」并刷新页面后再试。';
    }
    if (/stream not found/i.test(msg)) return '还没有这路流。请先推流，或把方式改成 play 并填写已存在的 app/stream。';
    if (!window.isSecureContext) return msg + '。请改用 ' + httpsAdminUrl();
    return msg;
}

async function getUserMediaFallback(needCam, needMic) {
    const camId = $('pushCamId') && $('pushCamId').value;
    const micId = $('pushMicId') && $('pushMicId').value;
    const video = needCam ? (camId ? { deviceId: { ideal: camId } } : true) : false;
    const audio = needMic ? (micId ? { deviceId: { ideal: micId } } : true) : false;
    const tryGum = (v, a) => navigator.mediaDevices.getUserMedia({ video: v, audio: a });
    try {
        return await tryGum(video, audio);
    } catch (e) {
        const missing = e && (e.name === 'NotFoundError' || /device not found/i.test(e.message || ''));
        if (needCam && needMic && missing) {
            try {
                const s = await tryGum(video, false);
                if ($('pushMic')) $('pushMic').checked = false;
                setPushStatus('未找到麦克风，已改为仅视频');
                return s;
            } catch (e2) { }
            try {
                const s = await tryGum(false, audio);
                if ($('pushCam')) $('pushCam').checked = false;
                setPushStatus('未找到摄像头，已改为仅音频');
                return s;
            } catch (e3) { }
        }
        throw e;
    }
}

async function acquireLocalStream() {
    const needScreen = $('pushScreen') && $('pushScreen').checked;
    const needCam = $('pushCam') && $('pushCam').checked;
    const needMic = $('pushMic') && $('pushMic').checked;
    if (!needScreen && !needCam && !needMic) throw new Error('请勾选摄像头、麦克风或屏幕/窗口');
    if (needScreen) {
        if (!navigator.mediaDevices.getDisplayMedia) throw new Error('当前浏览器不支持屏幕共享');
        const stream = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: false });
        if (needMic) {
            try {
                const mic = await getUserMediaFallback(false, true);
                mic.getAudioTracks().forEach((t) => stream.addTrack(t));
            } catch (e) {
                if ($('pushMic')) $('pushMic').checked = false;
                setPushStatus('屏幕已捕获，麦克风不可用: ' + gumError(e));
            }
        }
        return stream;
    }
    try {
        return await getUserMediaFallback(needCam, needMic);
    } catch (e) {
        const missing = e && (e.name === 'NotFoundError' || /device not found/i.test(e.message || ''));
        if (missing && navigator.mediaDevices.getDisplayMedia) {
            if ($('pushCam')) $('pushCam').checked = false;
            if ($('pushScreen')) $('pushScreen').checked = true;
            setPushStatus('未找到摄像头/麦克风，请选择要分享的屏幕或窗口');
            return navigator.mediaDevices.getDisplayMedia({ video: true, audio: false });
        }
        throw e;
    }
}

function parseRtcPlayUrl(raw) {
    raw = String(raw || '').trim();
    if (!raw) return null;
    if (/^webrtc:\/\//i.test(raw)) {
        const rest = raw.replace(/^webrtc:\/\//i, '');
        const slash = rest.indexOf('/');
        if (slash < 0) return null;
        const parts = rest.slice(slash + 1).split('/').filter(Boolean);
        if (parts.length >= 3) return { app: parts[1], stream: parts[2] };
        if (parts.length >= 2) return { app: parts[0], stream: parts[1] };
        return null;
    }
    try {
        const u = new URL(raw, location.origin);
        const app = u.searchParams.get('app');
        const stream = u.searchParams.get('stream');
        if (app && stream) return { app, stream };
    } catch (e) { }
    return null;
}

function applyPlayUrl(switchToPlay) {
    const parsed = parseRtcPlayUrl($('pushPlayUrl') && $('pushPlayUrl').value);
    if (!parsed) return null;
    if ($('pushApp')) $('pushApp').value = parsed.app;
    if ($('pushStream')) $('pushStream').value = parsed.stream;
    if (switchToPlay && $('pushType') && $('pushType').value !== 'play') $('pushType').value = 'play';
    return parsed;
}

function newPeer() {
    return newRtcPeer();
}

function waitIce(pc) {
    return waitHostIce(pc, 80);
}

async function webrtcSignal(pc, type, app, stream) {
    const recv = type !== 'push';
    const offer = await pc.createOffer({ offerToReceiveAudio: recv, offerToReceiveVideo: recv });
    await pc.setLocalDescription(offer);
    await waitIce(pc);
    const resp = await fetch(`/api/node/${encodeURIComponent(nodeId())}/webrtc?app=${encodeURIComponent(app)}&stream=${encodeURIComponent(stream)}&type=${encodeURIComponent(type)}&host=${encodeURIComponent(location.hostname)}`, {
        method: 'POST', headers: { 'Content-Type': 'text/plain' }, body: dropRelayIceSdp(pc.localDescription.sdp)
    });
    const ret = await resp.json();
    if (ret.code && ret.code !== 0) throw new Error(ret.msg || JSON.stringify(ret));
    const sdp = dropRelayIceSdp(ret.sdp || ret.data || '');
    if (!sdp) throw new Error('ZLM 未返回 SDP');
    await pc.setRemoteDescription({ type: 'answer', sdp });
    applyRtcLowDelay(pc);
    return ret;
}

function attachRemote(pc, videoEl) {
    const remote = new MediaStream();
    if (videoEl) videoEl.srcObject = remote;
    pc.ontrack = (ev) => {
        remote.addTrack(ev.track);
        if (videoEl) videoEl.play().catch(() => { });
    };
    return remote;
}

async function playRemote(app, stream, videoEl) {
    let lastErr;
    for (let i = 0; i < 10; i++) {
        const pc = newPeer();
        attachRemote(pc, videoEl);
        pc.addTransceiver('video', { direction: 'recvonly' });
        pc.addTransceiver('audio', { direction: 'recvonly' });
        try {
            await webrtcSignal(pc, 'play', app, stream);
            return pc;
        } catch (e) {
            try { pc.close(); } catch (e2) { }
            lastErr = e;
            if (!/stream not found/i.test((e && e.message) || '')) throw e;
            setPushStatus('等待源流就绪… (' + (i + 1) + '/10)');
            await new Promise((r) => setTimeout(r, 400));
        }
    }
    throw lastErr || new Error('stream not found');
}

async function bindLocalToPC(pc) {
    if (!window.isSecureContext) throw new Error('请先切到 HTTPS 再推流：' + httpsAdminUrl());
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) throw new Error('当前浏览器不支持 getUserMedia');
    pushLocal = await acquireLocalStream();
    if ($('localVideo')) {
        $('localVideo').srcObject = pushLocal;
        await $('localVideo').play().catch(() => { });
    }
    pushLocal.getTracks().forEach((t) => pc.addTrack(t, pushLocal));
    pushLocal.getVideoTracks().forEach((t) => {
        t.addEventListener('ended', () => setPushStatus('采集已结束（屏幕共享被关闭）'));
    });
}

function stopPush() {
    if (pushPC) { try { pushPC.close(); } catch (e) { } pushPC = null; }
    if (playPC) { try { playPC.close(); } catch (e) { } playPC = null; }
    if (pushLocal) {
        pushLocal.getTracks().forEach((t) => t.stop());
        pushLocal = null;
    }
    if ($('localVideo')) $('localVideo').srcObject = null;
    if ($('remoteVideo')) $('remoteVideo').srcObject = null;
    setPushStatus('已停止');
}

async function startPush() {
    stopPush();
    let type = ($('pushType') && $('pushType').value) || 'push';
    if (type === 'play') applyPlayUrl(false);
    const app = (($('pushApp') && $('pushApp').value) || 'live').trim();
    const stream = (($('pushStream') && $('pushStream').value) || 'webcam').trim();
    setPushStatus('协商中...');
    try {
        if (type === 'play') {
            playPC = await playRemote(app, stream, $('remoteVideo'));
            playPC.oniceconnectionstatechange = () => setPushStatus('ICE ' + playPC.iceConnectionState);
            setPushStatus('正在播放 ' + app + '/' + stream);
            return;
        }
        const pc = newPeer();
        pushPC = pc;
        pc.oniceconnectionstatechange = () => setPushStatus('ICE ' + pc.iceConnectionState);
        await bindLocalToPC(pc);
        await webrtcSignal(pc, 'push', app, stream);
        if (type === 'echo') {
            setPushStatus('已推流，正在拉回流…');
            playPC = await playRemote(app, stream, $('remoteVideo'));
            playPC.oniceconnectionstatechange = () => setPushStatus('ICE ' + playPC.iceConnectionState);
            setPushStatus('回显中 ' + app + '/' + stream + '（先推再拉）');
            return;
        }
        setPushStatus('正在推流 ' + app + '/' + stream);
    } catch (e) {
        setPushStatus('失败: ' + gumError(e));
    }
}

setInterval(() => { if (pageFromPath() === 'overview') loadCharts(); }, 30000);
window.openPlayer = openPlayer;

document.addEventListener('click', (e) => {
    const menu = $('userMenu');
    if (menu && menu.open && !menu.contains(e.target)) menu.open = false;
});
