/** Size native selects to their widest visible option without replacing the control. */
const states = new WeakMap();
const fontProperties = ['fontFamily', 'fontSize', 'fontStyle', 'fontWeight', 'fontStretch',
  'fontVariant', 'fontFeatureSettings', 'fontVariationSettings', 'letterSpacing', 'textTransform'];

function resizeSelect(el, state) {
  if (!state.active) return;
  const view = el.ownerDocument.defaultView;
  const style = view.getComputedStyle(el);
  const labels = Array.from(el.options)
    .filter(option => !option.hidden && option.style?.display !== 'none'
      && !(option.parentElement?.tagName === 'OPTGROUP' && option.parentElement.hidden))
    .map(option => option.text);
  const signature = JSON.stringify([labels, ...fontProperties.map(key => style[key]),
    style.paddingLeft, style.paddingRight, style.borderLeftWidth, style.borderRightWidth]);
  if (signature === state.signature) return;
  state.signature = signature;

  const measure = el.ownerDocument.createElement('span');
  Object.assign(measure.style, {
    position: 'fixed', visibility: 'hidden', whiteSpace: 'pre', pointerEvents: 'none',
    padding: '0', border: '0', margin: '0',
  });
  for (const key of fontProperties) measure.style[key] = style[key];
  el.ownerDocument.body.appendChild(measure);
  let textWidth = 0;
  for (const label of labels) {
    measure.textContent = label;
    textWidth = Math.max(textWidth, measure.getBoundingClientRect().width);
  }
  measure.remove();
  const number = value => Number.parseFloat(value) || 0;
  const chrome = number(style.paddingLeft) + number(style.paddingRight)
    + number(style.borderLeftWidth) + number(style.borderRightWidth);
  const arrow = Math.max(32, number(style.fontSize) * 2);
  el.style.width = `${Math.ceil(textWidth + chrome + arrow + 8)}px`;
}

function schedule(el) {
  const state = states.get(el);
  if (!state?.active || state.pending) return;
  state.pending = true;
  // Wait for Vue to finish rendering option labels before measurement.
  Promise.resolve().then(() => {
    state.pending = false;
    resizeSelect(el, state);
  });
}

export const contentSelectDirective = {
  inserted(el) {
    if (el.multiple || el.size > 1) return;
    const state = { active: true, pending: false, signature: '', update: () => schedule(el) };
    states.set(el, state);
    el.classList.add('content-select');
    el.addEventListener('change', state.update);
    el.ownerDocument.defaultView.addEventListener('resize', state.update);
    el.ownerDocument.fonts?.ready.then(() => {
      if (!state.active) return;
      state.signature = '';
      state.update();
    });
    schedule(el);
  },
  componentUpdated: schedule,
  unbind(el) {
    const state = states.get(el);
    if (!state) return;
    state.active = false;
    el.removeEventListener('change', state.update);
    el.ownerDocument.defaultView.removeEventListener('resize', state.update);
    states.delete(el);
  },
};
