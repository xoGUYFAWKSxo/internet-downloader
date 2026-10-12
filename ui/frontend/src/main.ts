import './style.css';
import {ProgressID} from '../wailsjs/go/main/App';

// The same program is the main window and, started with --progress=ID, one
// download's progress window.
ProgressID()
    .catch(() => 0)
    .then(async (id) => {
        if (id) (await import('./progress')).start(id);
        else await import('./list');
    })
    .finally(() => document.body.classList.add('ready'));
