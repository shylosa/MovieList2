export function bindAboutDialog(dialog, openButton, closeButton, returnFocus) {
    openButton.onclick = () => dialog.showModal();
    closeButton.onclick = () => dialog.close();
    dialog.addEventListener('click', event => {
        if (event.target !== dialog) return;
        const rect = dialog.getBoundingClientRect();
        if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) dialog.close();
    });
    dialog.addEventListener('close', () => returnFocus.focus());
}
