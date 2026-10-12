// Helpers shared by the main window and the progress window.

export const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

export function el(tag: string, cls: string, text: string) {
    const e = document.createElement(tag);
    e.className = cls;
    e.textContent = text;
    return e;
}

export function size(n: number) {
    if (n < 0) return 'size unknown';
    const units = ['bytes', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i === 0 ? n : n.toFixed(i >= 3 ? 2 : 1)) + ' ' + units[i];
}

export function duration(s: number) {
    s = Math.ceil(s);
    if (s < 60) return `${s} sec`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m} min ${s % 60} sec`;
    return `${Math.floor(m / 60)} hr ${m % 60} min`;
}

// msg is an error's text without the "Error: " that JavaScript puts first.
export const msg = (err: unknown) => String(err).replace(/^Error: /, '');

// Never shows 100% before the engine says the file is complete.
export const percent = (f: number) => `${(Math.floor(f * 1000) / 10).toFixed(1)}%`;
