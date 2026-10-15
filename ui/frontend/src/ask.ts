// The Download File Info box, like IDM's: where to save a link from the
// browser, with what its server said, before anything is downloaded.
import {AskInfo, ChooseFolder, SetSetting, ShowProgress, StartAsk} from '../wailsjs/go/main/App';
import {Quit, WindowSetTitle} from '../wailsjs/runtime/runtime';
import {$, msg, size} from './util';

const PAGE = `
<form class="ask" id="ask-form">
    <table class="pw-info">
        <tr><th>From</th><td id="ask-host"></td></tr>
        <tr><th><label for="ask-dir">Save into</label></th>
            <td class="ask-row"><input type="text" id="ask-dir" spellcheck="false"><button type="button" id="ask-browse">Browse…</button></td></tr>
        <tr><th><label for="ask-name">File name</label></th><td><input type="text" id="ask-name" spellcheck="false"></td></tr>
        <tr><th>Size</th><td id="ask-size"></td></tr>
        <tr><th>Resume capability</th><td id="ask-resume"></td></tr>
    </table>
    <p class="note" id="ask-note"></p>
    <p class="bad" id="ask-error"></p>
    <label class="check"><input type="checkbox" id="ask-off"> Don't show this box again</label>
    <div class="pw-buttons">
        <span class="spacer"></span>
        <button type="button" id="ask-later">Download Later</button>
        <button type="submit" class="primary" id="ask-start">Start Download</button>
        <button type="button" id="ask-cancel">Cancel</button>
    </div>
</form>`;

type Info = Awaited<ReturnType<typeof AskInfo>>;

export function start(id: number) {
    document.body.classList.add('progress-window');
    document.body.innerHTML = PAGE;
    const dir = $<HTMLInputElement>('ask-dir');
    const name = $<HTMLInputElement>('ask-name');
    const err = $('ask-error');
    let typed = false; // the user's own name wins over the server's
    let info: Info | null = null;
    name.addEventListener('input', () => { typed = true; });

    const show = (a: Info) => {
        info = a;
        $('ask-host').textContent = a.host || '—';
        if (!dir.value) dir.value = a.dir;
        if (!typed) name.value = a.name;
        $('ask-size').textContent = a.checking ? 'Checking…' : a.size >= 0 ? size(a.size) : 'Unknown';
        $('ask-resume').textContent = a.checking ? 'Checking…' : a.resume === 'yes' ? 'Yes' : 'No';
        $('ask-note').textContent = a.stream
            ? 'A video stream: its parts are downloaded and joined into one file, whose type is picked then.'
            : a.error ? `The server didn't answer properly: ${a.error} You can still try to download it.` : '';
        $<HTMLButtonElement>('ask-later').hidden = a.stream;
    };

    let busy = false;
    const answer = async (later: boolean) => {
        if (busy) return;
        busy = true;
        err.textContent = '';
        try {
            if ($<HTMLInputElement>('ask-off').checked) await SetSetting('ask', '0');
            const dl = await StartAsk(id, dir.value, name.value, later);
            if (!later) await ShowProgress(dl).catch(() => {});
            Quit();
        } catch (e) {
            err.textContent = msg(e);
            busy = false;
        }
    };

    $('ask-form').addEventListener('submit', (e) => { e.preventDefault(); answer(false); });
    $('ask-later').addEventListener('click', () => answer(true));
    $('ask-cancel').addEventListener('click', () => Quit());
    $('ask-browse').addEventListener('click', async () => {
        try {
            const d = await ChooseFolder(dir.value);
            if (d) dir.value = d;
        } catch (e) {
            err.textContent = msg(e);
        }
    });
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') Quit(); });
    document.addEventListener('contextmenu', (e) => {
        if (!(e.target instanceof HTMLInputElement)) e.preventDefault();
    });

    const refresh = async () => {
        try {
            show(await AskInfo(id));
        } catch (e) {
            err.textContent = msg(e);
            for (const b of ['ask-start', 'ask-later']) $<HTMLButtonElement>(b).disabled = true;
            return;
        }
        if (info?.checking) setTimeout(refresh, 300);
    };
    WindowSetTitle('Download File Info');
    refresh().then(() => { name.focus(); name.select(); });
}
