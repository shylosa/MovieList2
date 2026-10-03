export function bindSettingsMenu(button, menu, doc = document) {
    const anchor = button.closest('.settings-anchor');
    const close = (restoreFocus = false) => {
        menu.hidden = true;
        button.setAttribute('aria-expanded', 'false');
        if (restoreFocus) button.focus();
    };
    button.onclick = () => {
        const opening = menu.hidden;
        menu.hidden = !opening;
        button.setAttribute('aria-expanded', String(opening));
        if (opening) menu.querySelector('button').focus();
    };
    doc.addEventListener('click', event => {
        if (!anchor.contains(event.target)) close();
    });
    // focusin runs after the new element receives focus. A focusout microtask
    // can run between blur and focus, hiding a clicked item before its click.
    doc.addEventListener('focusin', event => {
        if (!anchor.contains(event.target)) close();
    });
    doc.addEventListener('keydown', event => {
        if (menu.hidden) return;
        if (event.key === 'Escape') { event.preventDefault(); close(true); }
        if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) && menu.contains(doc.activeElement)) {
            event.preventDefault();
            const buttons = [...menu.querySelectorAll('button')];
            const index = buttons.indexOf(doc.activeElement);
            const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length;
            buttons[next].focus();
        }
    });
    menu.addEventListener('click', event => { if (event.target.closest('button')) close(); });
    return close;
}
