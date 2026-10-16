// The countdown when the queue has finished and Options says to put the
// computer to sleep or turn it off. Closing it cancels, like Cancel.
import {CancelPower, PowerInfo, PowerNow} from '../wailsjs/go/main/App';
import {Quit, WindowSetTitle} from '../wailsjs/runtime/runtime';
import {$, msg} from './util';

const PAGE = `
<div class="pw power">
    <p class="pw-name">The queue has finished.</p>
    <p id="pow-text" aria-live="polite"></p>
    <p class="bad" id="pow-error"></p>
    <div class="pw-buttons">
        <span class="spacer"></span>
        <button id="pow-now"></button>
        <button id="pow-cancel" class="primary">Cancel</button>
    </div>
</div>`;

export function start() {
    document.body.innerHTML = PAGE;
    const err = $('pow-error');
    let failed = false;
    const tick = async () => {
        if (failed) return;
        try {
            const p = await PowerInfo();
            if (failed) return;
            if (!p.action) {
                Quit();
                return;
            }
            const sleep = p.action === 'sleep';
            const n = p.left ?? 0;
            $('pow-text').textContent = `The computer will ${sleep ? 'go to sleep' : 'turn off'} in ${n} second${n === 1 ? '' : 's'}.`;
            $('pow-now').textContent = sleep ? 'Sleep now' : 'Turn off now';
            WindowSetTitle(sleep ? 'Going to sleep' : 'Turning off');
        } catch (e) {
            err.textContent = msg(e);
        }
    };
    tick();
    setInterval(tick, 500);
    $('pow-cancel').addEventListener('click', () => CancelPower().finally(() => Quit()));
    $('pow-now').addEventListener('click', () => PowerNow().catch((e) => {
        failed = true;
        err.textContent = msg(e);
        $('pow-text').textContent = '';
        $<HTMLButtonElement>('pow-now').hidden = true;
        $('pow-cancel').textContent = 'Close';
    }));
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') CancelPower().finally(() => Quit()); });
    $('pow-cancel').focus();
}
