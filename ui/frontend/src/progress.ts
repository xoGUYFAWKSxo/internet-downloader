// The progress window of one download, like IDM's: what it is doing, and in
// the details, a map of the file and what each connection is doing.
import {DownloadDetail, OpenFile, PauseDownload, ResumeDownload, ShowDetails, ShowInFolder} from '../wailsjs/go/main/App';
import {Quit, WindowSetTitle} from '../wailsjs/runtime/runtime';
import {$, duration, el, msg, percent, size} from './util';

type Detail = Awaited<ReturnType<typeof DownloadDetail>>;
type Status = Detail['status'];

const PAGE = `
<div class="pw">
    <p class="pw-name" id="pw-name"></p>
    <table class="pw-info">
        <tr><th>Status</th><td id="pw-status"></td></tr>
        <tr><th>File size</th><td id="pw-size"></td></tr>
        <tr><th>Downloaded</th><td id="pw-done"></td></tr>
        <tr><th>Transfer rate</th><td id="pw-rate"></td></tr>
        <tr><th>Time left</th><td id="pw-left"></td></tr>
        <tr><th>Resume capability</th><td id="pw-resume"></td></tr>
        <tr><th>Server</th><td id="pw-server"></td></tr>
    </table>
    <div class="pw-bar"><div id="pw-fill"></div></div>
    <div class="pw-buttons">
        <button id="pw-details"></button>
        <span class="spacer"></span>
        <button id="pw-open" hidden>Open</button>
        <button id="pw-folder" hidden>Open folder</button>
        <button id="pw-pause">Pause</button>
        <button id="pw-close">Close</button>
    </div>
    <section id="pw-more" hidden>
        <canvas id="pw-map"></canvas>
        <p class="note" id="pw-map-note"></p>
        <div class="pw-conns-wrap">
            <table class="pw-conns">
                <thead><tr><th class="num">N.</th><th class="num">Downloaded</th><th>Info</th></tr></thead>
                <tbody id="pw-conns"></tbody>
            </table>
        </div>
    </section>
</div>`;

const RUNNING = new Set(['checking', 'downloading', 'joining', 'waiting']);

const CONN: Record<string, string> = {
    waiting: 'Waiting: the server allows fewer connections',
    starting: 'Connecting…',
    receiving: 'Receiving data…',
    busy: 'The server is busy; trying again soon',
    done: 'Finished its part',
};

const MODE: Record<string, string> = {
    multiplexed: 'streams on one connection',
    ranges: 'connections',
    single: 'one request',
};

function stateText(s: Status): string {
    switch (s.state) {
    case 'checking': return 'Connecting…';
    case 'downloading': return s.parts ? `Receiving segment ${s.partsDone ?? 0} of ${s.parts}` : 'Receiving data…';
    case 'joining': return 'Joining video and audio…';
    case 'waiting': return s.error || 'Waiting to try again';
    case 'paused': return s.error || 'Paused';
    case 'done': return 'Complete';
    case 'failed': return `Error: ${s.error ?? 'failed'}`;
    case 'cancelled': return s.error || 'Cancelled';
    }
    return s.state;
}

function fraction(s: Status): number | null {
    if (s.state === 'done') return 1;
    if (s.parts) return (s.partsDone ?? 0) / s.parts;
    return s.size > 0 ? s.done / s.size : null;
}

function stored(key: string): boolean {
    try { return localStorage.getItem(key) === '1'; } catch { return false; }
}
function store(key: string, v: boolean) {
    try { localStorage.setItem(key, v ? '1' : '0'); } catch { /* not kept */ }
}

export function start(id: number) {
    document.body.classList.add('progress-window');
    document.body.innerHTML = PAGE;

    let last: Detail | null = null;
    let details = stored('progress.details');
    // Speed is measured from what the engine reports, over at least 0.9 s.
    let sample: {done: number, at: number} | null = null;
    let speed: number | null = null;

    const showDetails = (on: boolean) => {
        details = on;
        store('progress.details', on);
        $('pw-more').hidden = !on;
        $('pw-details').textContent = on ? 'Hide details ▴' : 'Show details ▾';
        ShowDetails(on);
        if (last) drawMap(last);
    };
    showDetails(details);

    const act = (f: () => Promise<unknown>) => f().then(refresh).catch((err) => { $('pw-status').textContent = msg(err); });
    $('pw-details').addEventListener('click', () => showDetails(!details));
    $('pw-close').addEventListener('click', () => Quit());
    $('pw-pause').addEventListener('click', () => {
        if (!last) return;
        act(() => RUNNING.has(last!.status.state) ? PauseDownload(id) : ResumeDownload(id));
    });
    $('pw-open').addEventListener('click', () => last?.status.path && act(() => OpenFile(last!.status.path!)));
    $('pw-folder').addEventListener('click', () => last?.status.path && act(() => ShowInFolder(last!.status.path!)));
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') Quit(); });
    document.addEventListener('contextmenu', (e) => e.preventDefault());

    function measure(s: Status) {
        if (s.state !== 'downloading') {
            sample = null;
            speed = null;
            return;
        }
        const now = performance.now();
        if (!sample) {
            sample = {done: s.done, at: now};
        } else if (now - sample.at >= 900) {
            speed = (s.done - sample.done) / ((now - sample.at) / 1000);
            sample = {done: s.done, at: now};
        }
    }

    function show(d: Detail) {
        const s = d.status;
        measure(s);
        const f = fraction(s);
        const running = RUNNING.has(s.state);
        WindowSetTitle(f !== null && s.state !== 'done' ? `${percent(f)} ${s.name}` : s.name);
        $('pw-name').textContent = s.state === 'done' && s.path ? s.path : s.name;
        const st = $('pw-status');
        st.textContent = stateText(s);
        st.className = s.state === 'failed' ? 'bad' : s.state === 'done' ? 'ok' : '';
        $('pw-size').textContent = s.size >= 0 ? size(s.size) : 'Unknown';
        $('pw-done').textContent = s.parts ? `${s.partsDone ?? 0} of ${s.parts} segments` : f !== null ? `${size(s.done)} (${percent(f)})` : size(s.done);
        $('pw-rate').textContent = speed !== null ? `${size(Math.max(0, speed))}/sec` : '—';
        const left = speed && speed > 0 && s.size > 0 && !s.parts ? (s.size - s.done) / speed : null;
        $('pw-left').textContent = left !== null ? duration(left) : '—';
        $('pw-resume').textContent = d.resume === 'yes' ? 'Yes' : d.resume === 'no' ? 'No' : 'Not known yet';
        const how = s.protocol ? `, ${s.protocol}` + (running && s.streams && s.mode && s.mode !== 'single' ? `, ${s.streams} ${MODE[s.mode] ?? s.mode}` : '') : '';
        $('pw-server').textContent = (d.host || '—') + how;
        const fill = $('pw-fill');
        fill.style.width = `${(f ?? 0) * 100}%`;
        fill.className = s.state === 'failed' ? 'bad' : '';

        const done = s.state === 'done';
        $('pw-open').hidden = !done;
        $('pw-folder').hidden = !done;
        const pause = $<HTMLButtonElement>('pw-pause');
        pause.hidden = done;
        pause.textContent = running ? 'Pause' : 'Resume';
        pause.disabled = running ? s.state === 'joining' : !s.resumable;
        pause.title = running ? '' : s.resumable ? '' : "This download can't be resumed.";
        if (details) {
            drawMap(d);
            fillConns(d);
        }
    }

    // drawMap draws the file as a strip: written parts dark, the parts each
    // connection is still fetching light, and a mark where each one writes.
    function drawMap(d: Detail) {
        const s = d.status;
        const canvas = $<HTMLCanvasElement>('pw-map');
        const note = $('pw-map-note');
        if (s.parts || s.size <= 0) {
            canvas.hidden = true;
            note.textContent = s.parts
                ? 'A streamed video comes in many small segments, several at a time, so there is no map of the file.'
                : "The file's size isn't known, so there is no map of it.";
            return;
        }
        canvas.hidden = false;
        note.textContent = 'Green: written. Light blue: still to come over a connection. Blue line: where each connection is writing.';
        const css = getComputedStyle(document.documentElement);
        const color = (v: string) => css.getPropertyValue(v).trim();
        const w = canvas.clientWidth, h = canvas.clientHeight, r = devicePixelRatio || 1;
        canvas.width = Math.round(w * r);
        canvas.height = Math.round(h * r);
        const g = canvas.getContext('2d')!;
        g.scale(r, r);
        g.fillStyle = color('--bar-bg');
        g.fillRect(0, 0, w, h);
        const x = (n: number) => n / s.size * w;
        g.fillStyle = color('--accent');
        g.globalAlpha = 0.25;
        for (const c of d.conns) {
            if (c.pos >= 0 && c.end > c.pos) g.fillRect(x(c.pos), 0, x(c.end) - x(c.pos), h);
        }
        g.globalAlpha = 1;
        g.fillStyle = color('--bar');
        for (const sp of d.have) g.fillRect(x(sp.start), 0, Math.max(1, x(sp.end) - x(sp.start)), h);
        g.fillStyle = color('--accent');
        for (const c of d.conns) {
            if (c.state === 'receiving' && c.pos >= 0) g.fillRect(Math.min(w - 2, x(c.pos)), 0, 2, h);
        }
    }

    function fillConns(d: Detail) {
        const body = $('pw-conns');
        if (!d.conns.length) {
            const tr = document.createElement('tr');
            const td = el('td', 'note', d.status.parts ? 'Segments are fetched several at a time; they are not listed here.' : 'No connections are open.') as HTMLTableCellElement;
            td.colSpan = 3;
            tr.append(td);
            body.replaceChildren(tr);
            return;
        }
        body.replaceChildren(...d.conns.map((c, i) => {
            const tr = document.createElement('tr');
            tr.append(el('td', 'num', String(i + 1)), el('td', 'num', size(c.got)), el('td', '', CONN[c.state] ?? c.state));
            return tr;
        }));
    }

    let busy = false;
    async function refresh() {
        if (busy) return;
        busy = true;
        try {
            last = await DownloadDetail(id);
            show(last);
        } catch (err) {
            const text = msg(err);
            $('pw-status').textContent = text;
            $('pw-status').className = 'bad';
            $<HTMLButtonElement>('pw-pause').disabled = true;
            if (/no longer in the list/.test(text)) WindowSetTitle('Download removed');
        } finally {
            busy = false;
        }
    }
    refresh();
    setInterval(refresh, 500);
}
