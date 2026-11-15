// One shared, local-data popover for the editor's compact metadata controls.
export function createMetadataPopover(doc = document, viewport = window) {
    let current = null;
    let hideTimer;
    let sequence = 0;
    let suppressFocus = false;
    const focusTrigger = trigger => {
        suppressFocus = true;
        trigger.focus();
        suppressFocus = false;
    };
    const cancelHide = () => clearTimeout(hideTimer);
    const hide = () => {
        cancelHide();
        if (!current) return;
        current.trigger.setAttribute('aria-expanded', 'false');
        current.trigger.removeAttribute('aria-describedby');
        current.panel.remove();
        current = null;
    };
    const scheduleHide = () => {
        cancelHide();
        hideTimer = setTimeout(() => {
            if (current && !current.pinned && !current.panel.contains(doc.activeElement)) hide();
        }, 140);
    };
    const show = (trigger, title, text) => {
        cancelHide();
        if (current?.trigger === trigger) return;
        hide();
        const panel = doc.createElement('section');
        panel.className = 'inspector-metadata-popover';
        panel.id = `inspector-metadata-popover-${++sequence}`;
        panel.setAttribute('role', 'dialog');
        panel.setAttribute('aria-label', title);
        const header = doc.createElement('div');
        header.className = 'inspector-metadata-popover-header';
        const heading = doc.createElement('strong');
        heading.textContent = title;
        const close = doc.createElement('button');
        close.type = 'button';
        close.textContent = '✕';
        close.setAttribute('aria-label', 'Закрити перегляд');
        close.addEventListener('click', () => { hide(); focusTrigger(trigger); });
        const content = doc.createElement('div');
        content.className = 'inspector-metadata-popover-text';
        content.tabIndex = 0;
        content.textContent = text;
        header.append(heading, close);
        panel.append(header, content);
        panel.addEventListener('mouseenter', cancelHide);
        panel.addEventListener('mouseleave', scheduleHide);
        panel.addEventListener('focusout', scheduleHide);
        doc.body.append(panel);
        current = {trigger, panel, content, pinned: false};
        trigger.setAttribute('aria-expanded', 'true');
        trigger.setAttribute('aria-describedby', panel.id);
        const rect = trigger.getBoundingClientRect();
        panel.style.left = `${Math.max(8, Math.min(rect.left, viewport.innerWidth - panel.offsetWidth - 8))}px`;
        const top = rect.bottom + panel.offsetHeight + 8 <= viewport.innerHeight ? rect.bottom + 8 : rect.top - panel.offsetHeight - 8;
        panel.style.top = `${Math.max(8, top)}px`;
    };
    const attach = (trigger, title, text) => {
        trigger.setAttribute('aria-haspopup', 'dialog');
        trigger.setAttribute('aria-expanded', 'false');
        trigger.addEventListener('mouseenter', () => {
            if (!current?.pinned || current.trigger === trigger) show(trigger, title, text);
        });
        trigger.addEventListener('mouseleave', scheduleHide);
        trigger.addEventListener('focus', () => { if (!suppressFocus) show(trigger, title, text); });
        trigger.addEventListener('blur', scheduleHide);
        trigger.addEventListener('click', event => {
            if (current?.trigger === trigger && current.pinned) { hide(); return; }
            show(trigger, title, text);
            current.pinned = true;
            cancelHide();
            if (event.detail === 0) current.content.focus();
        });
    };
    doc.addEventListener('pointerdown', event => {
        if (current && !current.trigger.contains(event.target) && !current.panel.contains(event.target)) hide();
    });
    doc.addEventListener('keydown', event => {
        if (current && event.key === 'Escape') {
            event.preventDefault();
            event.stopPropagation();
            const trigger = current.trigger;
            hide();
            focusTrigger(trigger);
        }
    }, true);
    viewport.addEventListener('resize', hide);
    viewport.addEventListener('scroll', event => {
        if (current && !current.panel.contains(event.target)) hide();
    }, true);
    return {attach, hide};
}
