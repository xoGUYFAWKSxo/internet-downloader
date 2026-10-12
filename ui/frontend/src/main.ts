import './style.css';
import {CheckEngine} from '../wailsjs/go/main/App';

const statusEl = document.getElementById('engine-status')!;
const button = document.getElementById('recheck') as HTMLButtonElement;

async function refresh() {
    button.disabled = true;
    statusEl.className = '';
    statusEl.textContent = 'Checking the download engine…';
    try {
        const s = await CheckEngine();
        statusEl.className = s.running ? 'ok' : 'bad';
        statusEl.textContent = s.running ? `${s.message} Version ${s.version}.` : s.message;
    } catch (err) {
        statusEl.className = 'bad';
        statusEl.textContent = `Couldn't check the engine: ${err}`;
    } finally {
        button.disabled = false;
    }
}

button.addEventListener('click', refresh);
refresh();
